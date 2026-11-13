package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/ratelimit"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// The read-only HTTP surface: the A2A binding, the two JSON paths the A2A
// message wraps, and the agent card. It binds loopback only unless
// --bind-public is given, and a non-loopback bind requires a token.
const (
	// defaultServeAddr is the loopback endpoint `agent serve` binds.
	defaultServeAddr = "127.0.0.1:8080"
	// retrieveRoute is the deterministic retrieval path the future
	// `search_writing` tool hands back the same JSON.
	retrieveRoute = "/v1/retrieve"
	// askRoute runs the full grounded loop.
	askRoute = "/v1/ask"
	// defaultRateLimit is the per-token task budget per minute (PLAN.md
	// Phase 4): one peer must not be able to pin the single chat-model host.
	defaultRateLimit = 30
	// defaultAuthAttemptLimit caps failed bearer-auth attempts per address
	// per minute. A successful lookup is not charged; failures are charged
	// only after the lookup, so a valid token is never throttled behind NAT.
	defaultAuthAttemptLimit = 120
	// maxRequestBody caps one request body; a query is a sentence, not a
	// document.
	maxRequestBody = 64 << 10
	// retrieveTimeout bounds one retrieval request, embedding call included.
	retrieveTimeout = 30 * time.Second
	// shutdownTimeout bounds the graceful drain on SIGINT/SIGTERM.
	shutdownTimeout = 5 * time.Second
	// statusClientClosedRequest is the non-standard status (nginx 499) used
	// when the peer cancels a long request; the connection is already gone,
	// but access logs and metrics should not call it a gateway failure.
	statusClientClosedRequest = 499
	// surfaceRPC and surfaceJSON select the error body shape a transport
	// middleware writes: JSON-RPC for /a2a, the dev surface's {"error"} for
	// /v1/*.
	surfaceRPC  = "rpc"
	surfaceJSON = "json"
)

// runServe implements `agent serve`: the read-only HTTP surface over the
// existing index, including the A2A JSON-RPC endpoint at /a2a and the agent
// card at /.well-known/agent-card.json.
func runServe(args []string, rtCfg envcfg.Config, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", defaultServeAddr, "bind address (host:port)")
	bindPublic := fs.Bool("bind-public", false, "bind a non-loopback address; requires at least one A2A token")
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
	rateLimit := fs.Int("rate-limit", defaultRateLimit, "model-touching requests per minute per token (0 disables)")
	authLimit := fs.Int("auth-limit", defaultAuthAttemptLimit, "failed bearer-auth attempts per minute per address (0 disables)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent serve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *bindPublic {
		if err := validatePublicAddr(*addr); err != nil {
			fmt.Fprintf(stderr, "agent serve: %v\n", err)
			return 2
		}
	} else if err := validateLoopbackAddr(*addr); err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 2
	}
	if *rateLimit < 0 {
		fmt.Fprintf(stderr, "agent serve: --rate-limit must be >= 0 (0 disables)\n")
		return 2
	}
	if *authLimit < 0 {
		fmt.Fprintf(stderr, "agent serve: --auth-limit must be >= 0 (0 disables)\n")
		return 2
	}
	// Tokens are the only auth input. A missing token file and an empty env
	// list mean the token-less loopback dev deployment; a malformed file is a
	// startup error, never a silent fallback.
	tokens, err := auth.NewStore(rtCfg.A2ATokens, rtCfg.A2ATokensFile)
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 2
	}
	secured := tokens.Secured()
	if *bindPublic && !secured {
		fmt.Fprintf(stderr, "agent serve: --bind-public requires an A2A token; issue one with `agent tokens add --id <id>` or set %s\n", envcfg.EnvA2ATokens)
		return 2
	}

	// The card must never advertise an endpoint derived from an untrusted
	// Host header. Public exposure therefore requires an explicit root;
	// loopback derives one from the bind address.
	cardBase, err := cardBaseURL(*addr, *bindPublic, rtCfg.A2APublicURL)
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 2
	}

	// Read-only: serving a query must never create, migrate, or write the
	// index. A missing file is the "run ingest + embed first" case.
	st, err := store.OpenReadOnly(*dbFlag)
	if errors.Is(err, store.ErrNoDatabase) {
		fmt.Fprintf(stderr, "agent serve: no index at %s; run `agent ingest` and `agent embed` first\n", *dbFlag)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := verifyIndexModel(st, rtCfg.EmbedModel, int(rtCfg.EmbedDims)); err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}

	// Embeddings and chat each get their own endpoint and credential: the
	// embedding model may stay local while the chat model is a cloud provider.
	hc := shared.NewHTTPClient(embedHTTPTimeout)
	embedClient, err := llm.NewClient(rtCfg.EmbedBaseURL, rtCfg.EmbedAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}
	chatClient, err := llm.NewClient(rtCfg.ChatBaseURL, rtCfg.ChatAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}
	chatClient.TokenParam = rtCfg.ChatTokenParam
	searcher := &retrieval.Searcher{Store: st, Embedder: embedClient, Model: rtCfg.EmbedModel}
	loop := &agentloop.Runner{
		Searcher:   searcher,
		Chat:       chatClient,
		Model:      rtCfg.ChatModel,
		MaxElapsed: askTimeout,
	}
	limiter := ratelimit.New(*rateLimit)
	authAttempts := ratelimit.New(*authLimit)

	card := serveAgentCard(tokens.Secured, cardBase)
	mux := http.NewServeMux()
	for _, path := range a2a.CardPaths {
		mux.Handle("GET "+path, logging.RequestMiddleware(path, card))
	}
	mux.Handle(a2a.CardURLPath, logging.RequestMiddleware(a2a.CardURLPath,
		requireToken(tokens, surfaceRPC, authAttempts,
			limitTasks(limiter, surfaceRPC, a2a.NewHandler(loop)))))
	mux.Handle(retrieveRoute, logging.RequestMiddleware(retrieveRoute,
		requireToken(tokens, surfaceJSON, authAttempts,
			limitTasks(limiter, surfaceJSON, newRetrieveHandler(searcher)))))
	mux.Handle(askRoute, logging.RequestMiddleware(askRoute,
		requireToken(tokens, surfaceJSON, authAttempts,
			limitTasks(limiter, surfaceJSON, newAskHandler(loop)))))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      askTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Signals are only observed by the real command, not by handler tests.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := logging.Main()
	log.Info().
		Str("addr", *addr).
		Str("db", *dbFlag).
		Str("embed_model", rtCfg.EmbedModel).
		Int("embed_dims", st.Dims()).
		Str("chat_model", rtCfg.ChatModel).
		Bool("public_bind", *bindPublic).
		Bool("auth", secured).
		Int("tokens", tokens.Len()).
		Int("rate_limit_per_min", *rateLimit).
		Int("auth_limit_per_min", *authLimit).
		Msg("server listening")

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("server failed")
			fmt.Fprintf(stderr, "agent serve: %v\n", err)
			return 1
		}
	case <-ctx.Done():
		stop()
		log.Info().Msg("shutting down server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// A long request (ask can take ~20s) routinely outlives the drain
			// window on Ctrl-C. That is an expected forced close, not a
			// server failure.
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn().Err(err).Msg("server shutdown deadline exceeded; closing remaining connections")
			} else {
				log.Error().Err(err).Msg("server shutdown failed")
				fmt.Fprintf(stderr, "agent serve: shutdown: %v\n", err)
				return 1
			}
		}
	}
	return 0
}

// serveAgentCard answers the agent card requests. The card is public: a peer
// must be able to discover which scheme to authenticate with before it has a
// token. secured is evaluated per request so a live token-store change is
// reflected; baseURL is fixed at startup and never derived from a request.
func serveAgentCard(secured func() bool, baseURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(a2a.NewCard(baseURL, secured())); err != nil {
			log := logging.HTTP()
			log.Error().Err(err).Msg("write agent card failed")
		}
	})
}

// cardBaseURL resolves the service root the agent card advertises. Public
// binds require an explicit AGENT_A2A_PUBLIC_URL; loopback derives one from
// the bind address. Neither path trusts the request Host or forwarded headers.
func cardBaseURL(addr string, public bool, configured string) (string, error) {
	configured = strings.TrimRight(strings.TrimSpace(configured), "/")
	if configured != "" {
		u, err := url.Parse(configured)
		if err != nil {
			return "", fmt.Errorf("AGENT_A2A_PUBLIC_URL must be an absolute http(s) URL with a host")
		}
		scheme := strings.ToLower(u.Scheme)
		port := u.Port()
		portNum, portErr := strconv.Atoi(port)
		if scheme == "" || u.Hostname() == "" || (scheme != "http" && scheme != "https") ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(port != "" && (portErr != nil || portNum < 1 || portNum > 65535)) {
			// Never echo the value: a rejected URL may hold proxy userinfo
			// or a signed query, and config errors must not leak secrets.
			return "", fmt.Errorf("AGENT_A2A_PUBLIC_URL must be an absolute http(s) URL with a host")
		}
		return configured, nil
	}
	if public {
		return "", fmt.Errorf("--bind-public requires %s (the absolute http(s) URL peers should use)", envcfg.EnvA2APublicURL)
	}
	return "http://" + addr, nil
}

// validateLoopbackAddr rejects any address that could expose a token-less
// server beyond this host. An empty host (":8080") means all interfaces, so it
// is rejected explicitly rather than left to the listener.
func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("--addr %q binds every interface; use a loopback host such as 127.0.0.1, or --bind-public with a token", addr)
	}
	if err := validatePort(addr, port); err != nil {
		return err
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %q is not loopback; pass --bind-public and configure a token to expose the service", addr)
	}
	return nil
}

// validatePublicAddr accepts any bindable address once --bind-public is set;
// the token requirement is enforced separately.
func validatePublicAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	return validatePort(addr, port)
}

func validatePort(addr, port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid --addr %q: port must be a number between 1 and 65535", addr)
	}
	return nil
}

// requireToken enforces the bearer token once the store has ever held one.
// Emptying the token file at runtime does not disable auth: the store latches
// secured, so requests fail with 401 until a token is configured again. A
// store that started without tokens stays token-less for the loopback dev
// deployment (the caller has already refused a public bind in that case).
func requireToken(store *auth.Store, surface string, attempts *ratelimit.Limiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store == nil || !store.Secured() {
			next.ServeHTTP(w, r)
			return
		}
		key := "ip:" + remoteHost(r.RemoteAddr)
		token, ok := tokenFromRequest(store, r)
		if !ok {
			// Charge only failures, so a valid token is never throttled by an
			// attacker sharing its address. The lookup happens first: blocking
			// a tripped address before validation would let one peer lock out
			// every valid token behind the same NAT.
			if retry, allowed := allowKey(attempts, key); !allowed {
				writeRateLimitError(w, surface, key, retry)
				return
			}
			writeAuthError(w, surface)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithToken(r.Context(), token)))
	})
}

// tokenFromRequest extracts and validates the bearer credential.
func tokenFromRequest(store *auth.Store, r *http.Request) (auth.Token, bool) {
	value, ok := bearerToken(r)
	if !ok {
		return auth.Token{}, false
	}
	return store.Lookup(value)
}

// bearerToken extracts the RFC 6750 bearer credential.
func bearerToken(r *http.Request) (string, bool) {
	scheme, value, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	return value, value != ""
}

// writeAuthError writes the 401 both surfaces use, including the challenge
// header A2A clients look for.
func writeAuthError(w http.ResponseWriter, surface string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="elilap.dev-agent"`)
	if surface == surfaceRPC {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(a2a.Response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &a2a.Error{Code: a2a.CodeInvalidRequest, Message: "unauthorized"},
		})
		return
	}
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
}

// limitTasks applies the per-token task budget to the answer-bearing routes.
// An unauthenticated (dev) request is keyed by peer address so a local caller
// cannot monopolize the model host either.
func limitTasks(limiter *ratelimit.Limiter, surface string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := ""
		if token, ok := auth.TokenFrom(r.Context()); ok {
			key = token.ID
		} else {
			key = "ip:" + remoteHost(r.RemoteAddr)
		}
		if retry, ok := allowKey(limiter, key); !ok {
			writeRateLimitError(w, surface, key, retry)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allowKey is Allow with a nil-safe wrapper, so auth-failure limiting can be
// omitted in tests and in configurations that do not need it.
func allowKey(limiter *ratelimit.Limiter, key string) (time.Duration, bool) {
	if limiter == nil {
		return 0, true
	}
	return limiter.Allow(key)
}

// writeRateLimitError writes the 429 both surfaces use, with Retry-After and a
// JSON-RPC -32000 body on /a2a.
func writeRateLimitError(w http.ResponseWriter, surface, key string, retry time.Duration) {
	seconds := int(math.Ceil(retry.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	// Log at most once per key per minute: this path is reachable without
	// credentials, and an unbounded warning stream is its own DoS.
	if rateLimitLog.allow(surface + "|" + key) {
		log := logging.HTTP()
		log.Warn().Str("key", key).Int("retry_after_s", seconds).Msg("rate limit exceeded")
	}
	if surface == surfaceRPC {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(a2a.Response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error: &a2a.Error{
				Code:    a2a.CodeServerError,
				Message: "rate limit exceeded",
				Data:    map[string]any{"retry_after_seconds": seconds},
			},
		})
		return
	}
	writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
}

// keyedThrottle allows one log line per key per window.
type keyedThrottle struct {
	mu     sync.Mutex
	window time.Duration
	last   map[string]time.Time
}

func newKeyedThrottle(window time.Duration) *keyedThrottle {
	return &keyedThrottle{window: window, last: make(map[string]time.Time)}
}

// allow reports whether key may log now, and records the attempt.
func (t *keyedThrottle) allow(key string) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.window {
		return false
	}
	t.last[key] = now
	if len(t.last) > 4096 {
		for k, seen := range t.last {
			if now.Sub(seen) >= t.window {
				delete(t.last, k)
			}
		}
	}
	return true
}

// rateLimitLog bounds rate-limit warning lines per key.
var rateLimitLog = newKeyedThrottle(time.Minute)

// remoteHost strips the port from a RemoteAddr.
func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// retriever is the slice of retrieval.Searcher the HTTP handler needs, so
// handler tests can run without an index or a model server.
type retriever interface {
	Search(ctx context.Context, q retrieval.Query) ([]retrieval.Hit, error)
}

// retrieveRequest is the POST /v1/retrieve body: the `search_writing` params
// (query, top_n, source, platform, after, before, recency_halflife_days).
// Unknown fields are rejected, so a typo cannot silently widen the search.
type retrieveRequest struct {
	Query               string   `json:"query"`
	TopN                int      `json:"top_n"`
	Source              string   `json:"source"`
	Platform            []string `json:"platform"`
	After               string   `json:"after"`
	Before              string   `json:"before"`
	RecencyHalflifeDays float64  `json:"recency_halflife_days"`
}

type retrieveHandler struct {
	searcher retriever
}

func newRetrieveHandler(searcher retriever) http.Handler {
	return &retrieveHandler{searcher: searcher}
}

func (h *retrieveHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req retrieveRequest
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	// A second value means two objects were posted; rejecting keeps the
	// request's meaning unambiguous.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "body must contain exactly one JSON object")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), retrieveTimeout)
	defer cancel()

	hits, err := h.searcher.Search(ctx, retrieval.Query{
		Text:                req.Query,
		TopN:                req.TopN,
		Source:              req.Source,
		Platforms:           req.Platform,
		After:               req.After,
		Before:              req.Before,
		RecencyHalflifeDays: req.RecencyHalflifeDays,
	})
	log := logging.HTTP()
	if err != nil {
		switch {
		case errors.Is(err, retrieval.ErrInvalidQuery):
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, context.Canceled):
			writeJSONError(w, statusClientClosedRequest, "request canceled")
		case errors.Is(err, context.DeadlineExceeded):
			log.Error().Err(err).Msg("retrieve timed out")
			writeJSONError(w, http.StatusGatewayTimeout, "retrieval timed out")
		default:
			// The detail stays in the log; the client gets a stable shape.
			log.Error().Err(err).Msg("retrieve failed")
			writeJSONError(w, http.StatusBadGateway, "retrieval failed")
		}
		return
	}
	if hits == nil {
		hits = []retrieval.Hit{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(hits); err != nil {
		log.Error().Err(err).Msg("write retrieve response failed")
	}
}

// writeJSONError writes the one error shape the /v1 surface uses.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// answerer is the loop entry point the ask handler needs, so handler tests can
// run without a model server or an index.
type answerer interface {
	Run(ctx context.Context, question string) (agentloop.Answer, error)
}

// askRequest is the POST /v1/ask body.
type askRequest struct {
	Question string `json:"question"`
}

type askHandler struct {
	loop answerer
}

func newAskHandler(loop answerer) http.Handler {
	return &askHandler{loop: loop}
}

func (h *askHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONError(w, http.StatusMethodNotAllowed, "use POST")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var req askRequest
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "body must contain exactly one JSON object")
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeJSONError(w, http.StatusBadRequest, "question must not be empty")
		return
	}
	if len(req.Question) > agentloop.MaxQuestionBytes {
		writeJSONError(w, http.StatusBadRequest,
			fmt.Sprintf("question must be at most %d bytes", agentloop.MaxQuestionBytes))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()

	start := time.Now()
	answer, err := h.loop.Run(ctx, req.Question)
	tokenID := ""
	if token, ok := auth.TokenFrom(r.Context()); ok {
		tokenID = token.ID
	}
	entry := audit.Entry{
		TokenID:  tokenID,
		Method:   "v1/ask",
		Question: req.Question,
		Elapsed:  time.Since(start),
	}
	log := logging.HTTP()
	if err != nil {
		switch {
		case errors.Is(err, agentloop.ErrQuestionTooLong):
			entry.Outcome = audit.OutcomeError
			audit.Answer(entry)
			writeJSONError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, context.Canceled):
			entry.Outcome = audit.OutcomeCanceled
			audit.Answer(entry)
			writeJSONError(w, statusClientClosedRequest, "request canceled")
		case errors.Is(err, agentloop.ErrBudget), errors.Is(err, context.DeadlineExceeded):
			entry.Outcome = audit.OutcomeTimeout
			audit.Answer(entry)
			log.Error().Err(err).Msg("ask timed out")
			writeJSONError(w, http.StatusGatewayTimeout, "answer timed out")
		default:
			entry.Outcome = audit.OutcomeError
			audit.Answer(entry)
			log.Error().Err(err).Msg("ask failed")
			writeJSONError(w, http.StatusBadGateway, "answer failed")
		}
		return
	}

	entry.ToolIterations = answer.ToolIterations
	entry.Citations = len(answer.Citations)
	entry.Sources = len(answer.Sources)
	entry.Refused = answer.Refused
	if answer.Refused {
		entry.Outcome = audit.OutcomeRefused
	} else {
		entry.Outcome = audit.OutcomeAnswered
	}
	audit.Answer(entry)

	if answer.Citations == nil {
		answer.Citations = []agentloop.Citation{}
	}
	if answer.Sources == nil {
		answer.Sources = []agentloop.Source{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		log.Error().Err(err).Msg("write ask response failed")
	}
}
