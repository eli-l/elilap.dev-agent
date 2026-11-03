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

// The Phase 2 dev server: token-less by design, so it must bind loopback only
// (PLAN.md Decision 16). Auth and rate limits are Phase 4 work.
const (
	// defaultServeAddr is the loopback endpoint `agent serve` binds.
	defaultServeAddr = "127.0.0.1:8080"
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
// existing index. It binds loopback only and refuses to start otherwise.
func runServe(args []string, rtCfg envcfg.Config, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", defaultServeAddr, "loopback bind address (host:port)")
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
	if err := validateLoopbackAddr(*addr); err != nil {
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
		Str("model", rtCfg.EmbedModel).
		Msg("dev server listening (token-less, loopback-only)")

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

// validateLoopbackAddr rejects any address that could expose the token-less
// server beyond this host. An empty host (":8080") means all interfaces, so it
// is rejected explicitly rather than left to the listener.
func validateLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("--addr %q binds every interface; use a loopback host such as 127.0.0.1", addr)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid --addr %q: port must be a number between 1 and 65535", addr)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %q is not loopback; the dev server is token-less and must not be exposed", addr)
	}
	return nil
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
