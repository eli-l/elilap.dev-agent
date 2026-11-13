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
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/audit"
	"github.com/eli-l/elilap.dev-agent/internal/auth"
	"github.com/eli-l/elilap.dev-agent/internal/budget"
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
	// defaultRateLimit is the /v1/retrieve embedding bucket per minute. The
	// answer surfaces use the smaller defaultAskRate below, because a minute
	// rate cannot bound a model bill and the daily budget gates do.
	defaultRateLimit = 30
	// defaultAskRate is the answers allowed per minute per identity.
	defaultAskRate = 3
	// defaultAskQuota is the answers allowed per day per identity: one leaked
	// or rotated id cannot drain the day.
	defaultAskQuota = 20
	// defaultAddressQuota is the answers allowed per day per peer address. It
	// is the backstop for a peer that rotates its self-asserted identity.
	defaultAddressQuota = 20
	// defaultGlobalQuota is the answers allowed per day across every peer: the
	// spend ceiling a public bind must declare.
	defaultGlobalQuota = 60
	// defaultMaxInflight caps concurrent answers so a queue cannot outlive the
	// server's write timeout.
	defaultMaxInflight = 2
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
	bindPublic := fs.Bool("bind-public", false, "bind a non-loopback address; requires AGENT_A2A_PUBLIC_URL and non-zero daily quotas")
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
	rateLimit := fs.Int("rate-limit", defaultRateLimit, "embedding requests per minute for /v1/retrieve (0 disables)")
	authLimit := fs.Int("auth-limit", defaultAuthAttemptLimit, "failed bearer-auth attempts per minute per address (0 disables)")
	askRate := fs.Int("ask-rate", defaultAskRate, "answers per minute per identity (0 disables)")
	askQuota := fs.Int("ask-quota", defaultAskQuota, "answers per day per identity (0 disables; required with --bind-public)")
	addressQuota := fs.Int("address-quota", defaultAddressQuota, "answers per day per peer address (0 disables; required with --bind-public)")
	globalQuota := fs.Int("quota-global", defaultGlobalQuota, "answers per day across all peers: the spend ceiling (0 disables; required with --bind-public)")
	maxInflight := fs.Int("max-inflight", defaultMaxInflight, "concurrent answers (0 disables)")
	quotaFile := fs.String("quota-file", "", "budget counter file (default <db dir>/quota.json; used when the budget is on)")
	allowPublicRetrieve := fs.Bool("allow-public-retrieve", false, "serve /v1/retrieve on a public bind (private debug path by default)")
	maxTokens := fs.Int("max-tokens", agentloop.DefaultMaxTokens, "completion token budget per chat request")
	excerptTokens := fs.Int("excerpt-tokens", agentloop.DefaultExcerptTokens, "total excerpt token budget")
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
	budgetOn := budgetActive(*bindPublic, fs)
	if *bindPublic {
		if err := validatePublicAddr(*addr); err != nil {
			fmt.Fprintf(stderr, "agent serve: %v\n", err)
			return 2
		}
	} else if err := validateLoopbackAddr(*addr); err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 2
	}
	for _, limit := range []struct {
		flag  string
		value int
	}{
		{"--rate-limit", *rateLimit},
		{"--auth-limit", *authLimit},
		{"--ask-rate", *askRate},
		{"--ask-quota", *askQuota},
		{"--address-quota", *addressQuota},
		{"--quota-global", *globalQuota},
		{"--max-inflight", *maxInflight},
	} {
		if limit.value < 0 {
			fmt.Fprintf(stderr, "agent serve: %s must be >= 0 (0 disables)\n", limit.flag)
			return 2
		}
	}
	if *maxTokens <= 0 {
		fmt.Fprintf(stderr, "agent serve: --max-tokens must be > 0\n")
		return 2
	}
	if *excerptTokens < 0 {
		fmt.Fprintf(stderr, "agent serve: --excerpt-tokens must be >= 0 (0 uses the default)\n")
		return 2
	}
	// A public agent is a spending surface: a peer with a URL and a question
	// can move real money on a cloud chat model. Refusing a bind without a
	// daily ceiling is the one configuration this service will not run.
	if *bindPublic && (*askQuota <= 0 || *addressQuota <= 0 || *globalQuota <= 0) {
		fmt.Fprintf(stderr, "agent serve: --bind-public requires --ask-quota, --address-quota, and --quota-global (a public agent needs a spend ceiling)\n")
		return 2
	}
	// Tokens are optional: a token-less loopback bind is the dev deployment and a
	// token-less public bind is the budgeted public one (Decision 25). A missing
	// token file and an empty env list mean no tokens; a malformed file is a
	// startup error, never a silent fallback.
	tokens, err := auth.NewStore(rtCfg.A2ATokens, rtCfg.A2ATokensFile)
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 2
	}
	secured := tokens.Secured()

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

	// The spend budget lives beside the index because the index itself is opened
	// read-only: a public answer must be counted somewhere that survives a
	// restart, and the index is derived data the operator may delete. A loopback
	// bind with no budget flag gets no limiter and no counter file at all.
	var spend *budget.Limiter
	quotaPath := ""
	if budgetOn {
		quotaPath = strings.TrimSpace(*quotaFile)
		if quotaPath == "" {
			quotaPath = filepath.Join(filepath.Dir(*dbFlag), "quota.json")
		}
		spend, err = budget.New(budget.Config{
			Bounds: budget.Bounds{
				PerMinute:        *askRate,
				PerDay:           *askQuota,
				PerAddressPerDay: *addressQuota,
				GlobalPerDay:     *globalQuota,
				MaxInflight:      *maxInflight,
			},
			Path: quotaPath,
		})
		if err != nil {
			fmt.Fprintf(stderr, "agent serve: %v\n", err)
			return 1
		}
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
		Searcher:      searcher,
		Chat:          chatClient,
		Model:         rtCfg.ChatModel,
		MaxElapsed:    askTimeout,
		MaxTokens:     *maxTokens,
		ExcerptTokens: *excerptTokens,
	}
	// The embedding bucket covers /v1/retrieve; answers are limited by spend.
	limiter := ratelimit.New(*rateLimit)
	authAttempts := ratelimit.New(*authLimit)

	card := serveAgentCard(tokens.Secured, cardBase)
	mux := http.NewServeMux()
	for _, path := range a2a.CardPaths {
		mux.Handle("GET "+path, logging.RequestMiddleware(path, card))
	}
	a2aHandler := a2a.NewHandler(loop)
	if spend != nil {
		a2aHandler.Budget = spend
	}
	mux.Handle(a2a.CardURLPath, logging.RequestMiddleware(a2a.CardURLPath,
		requireToken(tokens, surfaceRPC, authAttempts, a2aHandler)))
	// /v1/retrieve is a private debugging path: the loop already retrieves
	// internally, so a public bind must not expose an embedding endpoint that
	// only spends money and invites abuse.
	if !*bindPublic || *allowPublicRetrieve {
		mux.Handle(retrieveRoute, logging.RequestMiddleware(retrieveRoute,
			requireToken(tokens, surfaceJSON, authAttempts,
				limitRetrieval(limiter, surfaceJSON, newRetrieveHandler(searcher)))))
	}
	mux.Handle(askRoute, logging.RequestMiddleware(askRoute,
		requireToken(tokens, surfaceJSON, authAttempts,
			newAskHandler(loop, spend))))

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
		Str("quota_file", quotaPath).
		Bool("budget_enabled", spend != nil).
		Int("ask_rate_per_min", *askRate).
		Int("ask_quota_per_day", *askQuota).
		Int("address_quota_per_day", *addressQuota).
		Int("quota_global_per_day", *globalQuota).
		Int("max_inflight", *maxInflight).
		Bool("public_retrieve", !*bindPublic || *allowPublicRetrieve).
		Int("max_tokens", *maxTokens).
		Int("excerpt_tokens", *excerptTokens).
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

// budgetFlags are the flags that turn the spend budget on for a loopback bind.
// `--quota-file` alone does not: it only relocates the counters.
var budgetFlags = map[string]bool{
	"ask-rate":      true,
	"ask-quota":     true,
	"address-quota": true,
	"quota-global":  true,
	"max-inflight":  true,
}

// budgetActive reports whether the spend budget applies to this bind. A public
// bind is always budgeted; a loopback bind is the dev/eval surface (evals/ and
// tests-ai/ run their whole suite against it) and stays uncapped unless the
// operator asks for a budget explicitly.
func budgetActive(bindPublic bool, fs *flag.FlagSet) bool {
	if bindPublic {
		return true
	}
	active := false
	fs.Visit(func(f *flag.Flag) {
		if budgetFlags[f.Name] {
			active = true
		}
	})
	return active
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

// limitRetrieval applies the embedding bucket to the private /v1/retrieve
// route. Answers are bounded by spend in internal/budget instead, because a
// minute rate cannot cap a model bill.
func limitRetrieval(limiter *ratelimit.Limiter, surface string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := "ip:" + remoteHost(r.RemoteAddr)
		if token, ok := auth.TokenFrom(r.Context()); ok {
			key = "token:" + token.ID
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

// writeRateLimitError is the minute-bucket refusal (the embedding bucket and
// failed auth); it is a budget refusal with the rate scope.
func writeRateLimitError(w http.ResponseWriter, surface, key string, retry time.Duration) {
	writeBudgetError(w, surface, key, &budget.Refusal{Scope: budget.ScopeRate, RetryAfter: retry})
}

// writeBudgetError writes the 429 both surfaces use, with Retry-After and a
// JSON-RPC -32000 body carrying the scope on /a2a.
func writeBudgetError(w http.ResponseWriter, surface, key string, refusal *budget.Refusal) {
	seconds := int(math.Ceil(refusal.RetryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	// Log at most once per key per minute: this path is reachable without
	// credentials, and an unbounded warning stream is its own DoS.
	if rateLimitLog.Allow(surface + "|" + key) {
		log := logging.HTTP()
		log.Warn().
			Str("key", key).
			Str("scope", string(refusal.Scope)).
			Int("retry_after_s", seconds).
			Msg("budget refused")
	}
	if surface == surfaceRPC {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_ = json.NewEncoder(w).Encode(a2a.Response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error: &a2a.Error{
				Code:    a2a.CodeServerError,
				Message: refusal.Message(),
				Data: map[string]any{
					"reason":              "quota_exceeded",
					"scope":               string(refusal.Scope),
					"retry_after_seconds": seconds,
				},
			},
		})
		return
	}
	writeJSONError(w, http.StatusTooManyRequests, refusal.Message())
}

// rateLimitLog bounds rate-limit warning lines per key.
var rateLimitLog = logging.NewThrottle(time.Minute)

// remoteHost strips the port from a RemoteAddr.
func remoteHost(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// askCaller resolves who a /v1/ask call is charged to: a configured bearer
// token is the only verified identity, then the self-asserted X-User-Id, then
// the peer address. The address is charged alongside the principal, so
// rotating an id cannot escape the per-address gate.
func askCaller(r *http.Request) (budget.Caller, error) {
	principal := budget.Principal{Kind: budget.KindAddr, ID: budget.PeerAddress(r)}
	if token, ok := auth.TokenFrom(r.Context()); ok {
		principal = budget.Principal{Kind: budget.KindToken, ID: token.ID}
	} else if id := strings.TrimSpace(r.Header.Get("X-User-Id")); id != "" {
		if !budget.ValidID(id) {
			return budget.Caller{}, fmt.Errorf("X-User-Id must match [A-Za-z0-9._-]{1,64}")
		}
		principal = budget.Principal{Kind: budget.KindUser, ID: id}
	}
	return budget.Caller{Principal: principal, Address: budget.PeerAddress(r)}, nil
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
	// spend charges one answer to the caller; nil disables the budget.
	spend *budget.Limiter
}

func newAskHandler(loop answerer, spend *budget.Limiter) http.Handler {
	return &askHandler{loop: loop, spend: spend}
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

	caller, err := askCaller(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	release, err := h.spend.Acquire(caller)
	if err != nil {
		if refusal, ok := budget.AsRefusal(err); ok {
			// Throttle by the peer address, not the self-asserted principal: a
			// peer rotating X-User-Id would otherwise mint a fresh log key per
			// request and defeat the throttle.
			writeBudgetError(w, surfaceJSON, "ip:"+budget.PeerAddress(r), refusal)
			return
		}
		log := logging.HTTP()
		log.Error().Err(err).Msg("ask budget unavailable")
		writeJSONError(w, http.StatusBadGateway, "answer failed")
		return
	}
	defer release()

	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()

	start := time.Now()
	answer, err := h.loop.Run(ctx, req.Question)
	tokenID := ""
	if token, ok := auth.TokenFrom(r.Context()); ok {
		tokenID = token.ID
	}
	entry := audit.Entry{
		Principal: caller.Principal.String(),
		TokenID:   tokenID,
		Method:    "v1/ask",
		Question:  req.Question,
		Elapsed:   time.Since(start),
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
