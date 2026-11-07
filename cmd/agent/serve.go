package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// The Phase 2 dev server: token-less by design, so it binds loopback by
// default and needs an explicit --allow-public for any other host
// (PLAN.md Decision 16). Auth and rate limits are Phase 4 work.
const (
	// defaultServeHost and defaultServePort are the loopback endpoint
	// `agent serve` binds when no flags are given.
	defaultServeHost = "127.0.0.1"
	defaultServePort = 8080
	// retrieveRoute is the deterministic retrieval path the future
	// `search_writing` tool hands back the same JSON.
	retrieveRoute = "/v1/retrieve"
	// askRoute runs the full grounded loop.
	askRoute = "/v1/ask"
	// maxRequestBody caps one request body; a query is a sentence, not a
	// document.
	maxRequestBody = 64 << 10
	// retrieveTimeout bounds one retrieval request, embedding call included.
	retrieveTimeout = 30 * time.Second
	// shutdownTimeout bounds the graceful drain on SIGINT/SIGTERM.
	shutdownTimeout = 5 * time.Second
)

// runServe implements `agent serve`: the read-only dev HTTP surface over the
// existing index. It binds loopback by default and needs --allow-public for
// any other host.
func runServe(args []string, rtCfg envcfg.Config, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	host := fs.String("host", defaultServeHost, "bind host; 0.0.0.0 binds every interface (requires --allow-public)")
	port := fs.Int("port", defaultServePort, "bind port (1-65535)")
	allowPublic := fs.Bool("allow-public", false, "allow a non-loopback --host; the dev server is token-less")
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
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
	addr, err := resolveServeAddr(*host, *port, *allowPublic)
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

	if err := verifyIndexModel(st, rtCfg.EmbedModel); err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}

	client, err := llm.NewClient(rtCfg.LocalAIBaseURL, rtCfg.LocalAIAPIKey,
		shared.NewHTTPClient(embedHTTPTimeout))
	if err != nil {
		fmt.Fprintf(stderr, "agent serve: %v\n", err)
		return 1
	}
	searcher := &retrieval.Searcher{Store: st, Embedder: client, Model: rtCfg.EmbedModel}
	loop := &agentloop.Runner{
		Searcher:   searcher,
		Chat:       client,
		Model:      rtCfg.ChatModel,
		MaxElapsed: askTimeout,
	}

	mux := http.NewServeMux()
	mux.Handle(retrieveRoute, logging.RequestMiddleware(retrieveRoute, newRetrieveHandler(searcher)))
	mux.Handle(askRoute, logging.RequestMiddleware(askRoute, newAskHandler(loop)))

	srv := &http.Server{
		Addr:              addr,
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
	binding := "loopback-only"
	if !isLoopbackHost(*host) {
		log.Warn().
			Str("addr", addr).
			Msg("dev server is token-less and reachable beyond this host; keep it on a trusted network")
		binding = "non-loopback"
	}
	log.Info().
		Str("addr", addr).
		Str("db", *dbFlag).
		Str("model", rtCfg.EmbedModel).
		Msg("dev server listening (token-less, " + binding + ")")

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error().Err(err).Msg("dev server failed")
			fmt.Fprintf(stderr, "agent serve: %v\n", err)
			return 1
		}
	case <-ctx.Done():
		stop()
		log.Info().Msg("shutting down dev server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// A long request (ask can take ~20s) routinely outlives the drain
			// window on Ctrl-C. That is an expected forced close, not a
			// server failure.
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn().Err(err).Msg("dev server shutdown deadline exceeded; closing remaining connections")
			} else {
				log.Error().Err(err).Msg("dev server shutdown failed")
				fmt.Fprintf(stderr, "agent serve: shutdown: %v\n", err)
				return 1
			}
		}
	}
	return 0
}

// resolveServeAddr turns the --host/--port flags into a listen address. The
// server is token-less, so a host that is not loopback is refused unless
// --allow-public says the caller deliberately wants a reachable dev server
// (a container bridge, a LAN address, or 0.0.0.0 for every interface).
func resolveServeAddr(host string, port int, allowPublic bool) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		// net.Listen treats an empty host as every interface; make that an
		// explicit choice rather than a flag-forgetting accident.
		return "", errors.New("--host must not be empty; use 0.0.0.0 to bind every interface")
	}
	if port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid --port %d: port must be between 1 and 65535", port)
	}
	if !isLoopbackHost(host) && !allowPublic {
		return "", fmt.Errorf("--host %q is not loopback; the dev server is token-less. "+
			"Pass --allow-public to bind it on a trusted network", host)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// isLoopbackHost reports whether host names this machine only: the literal
// localhost or any loopback IP (127.0.0.0/8, ::1).
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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

// writeJSONError writes the one error shape the dev surface uses.
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

	ctx, cancel := context.WithTimeout(r.Context(), askTimeout)
	defer cancel()
	answer, err := h.loop.Run(ctx, req.Question)
	log := logging.HTTP()
	if err != nil {
		switch {
		case errors.Is(err, agentloop.ErrBudget), errors.Is(err, context.DeadlineExceeded):
			log.Error().Err(err).Msg("ask timed out")
			writeJSONError(w, http.StatusGatewayTimeout, "answer timed out")
		default:
			log.Error().Err(err).Msg("ask failed")
			writeJSONError(w, http.StatusBadGateway, "answer failed")
		}
		return
	}
	if answer.Citations == nil {
		answer.Citations = []agentloop.Citation{}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(answer); err != nil {
		log.Error().Err(err).Msg("write ask response failed")
	}
}
