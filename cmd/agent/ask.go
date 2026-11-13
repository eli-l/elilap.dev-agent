package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// askTimeout bounds one CLI answer end to end. The loop also carries this as
// its MaxElapsed budget, so a slow model stops before the context does.
const askTimeout = 90 * time.Second

// runAsk implements `agent ask`: the grounded tool-calling loop over the local
// index, with the same prompt, budgets, and refusal behavior as POST /v1/ask.
func runAsk(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.DBPath, "path to the SQLite index")
	model := fs.String("model", rtCfg.EmbedModel, "embeddings model id")
	chatModel := fs.String("chat-model", rtCfg.ChatModel, "chat model id")
	format := fs.String("format", "text", "output format: text or json")
	maxIterations := fs.Int("max-iterations", agentloop.DefaultMaxIterations, "maximum tool iterations")
	excerptTokens := fs.Int("excerpt-tokens", agentloop.DefaultExcerptTokens, "total excerpt token budget")
	maxTokens := fs.Int("max-tokens", agentloop.DefaultMaxTokens, "completion token budget per chat request")
	timeout := fs.Duration("timeout", askTimeout, "overall answer timeout")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Fprintf(stderr, "agent ask: provide a question, for example: agent ask \"what did I write about harnesses?\"\n")
		return 2
	}
	if *format != "text" && *format != "json" {
		fmt.Fprintf(stderr, "agent ask: --format must be text or json, got %q\n", *format)
		return 2
	}
	if *maxIterations <= 0 || *excerptTokens <= 0 || *maxTokens <= 0 {
		fmt.Fprintf(stderr, "agent ask: iteration and token budgets must be positive\n")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintf(stderr, "agent ask: --timeout must be positive\n")
		return 2
	}

	st, err := store.OpenReadOnly(*dbFlag)
	if errors.Is(err, store.ErrNoDatabase) {
		fmt.Fprintf(stderr, "agent ask: no index at %s; run `agent ingest` and `agent embed` first\n", *dbFlag)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}
	defer st.Close()

	if err := verifyIndexModel(st, *model, int(rtCfg.EmbedDims)); err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}

	// One request must fit inside the overall budget, so a raised --timeout
	// also extends a slow chat or embeddings call instead of failing at the
	// embed transport's 120s ceiling. The two roles get their own endpoint and
	// credential: embeddings can stay on the local host while the chat model
	// is a cloud provider (or the other way round).
	hc := shared.NewHTTPClient(*timeout)
	embedClient, err := llm.NewClient(rtCfg.EmbedBaseURL, rtCfg.EmbedAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}
	chatClient, err := llm.NewClient(rtCfg.ChatBaseURL, rtCfg.ChatAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}
	chatClient.TokenParam = rtCfg.ChatTokenParam
	searcher := &retrieval.Searcher{Store: st, Embedder: embedClient, Model: *model}
	runner := &agentloop.Runner{
		Searcher:      searcher,
		Chat:          chatClient,
		Model:         *chatModel,
		MaxIterations: *maxIterations,
		ExcerptTokens: *excerptTokens,
		MaxElapsed:    *timeout,
		MaxTokens:     *maxTokens,
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	answer, err := runner.Run(ctx, question)
	if err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}
	if err := printAnswer(stdout, *format, answer); err != nil {
		fmt.Fprintf(stderr, "agent ask: %v\n", err)
		return 1
	}
	return 0
}

// printAnswer renders the loop result. json is the same object POST /v1/ask
// returns; text is the human view over it.
func printAnswer(w io.Writer, format string, answer agentloop.Answer) error {
	switch format {
	case "json":
		return json.NewEncoder(w).Encode(answer)
	case "text":
		fmt.Fprintf(w, "answer: %s\n", answer.Answer)
		fmt.Fprintf(w, "refused: %t\n", answer.Refused)
		fmt.Fprintf(w, "tool iterations: %d\n", answer.ToolIterations)
		fmt.Fprintf(w, "elapsed: %dms\n", answer.ElapsedMS)
		if len(answer.Sources) > 0 {
			fmt.Fprintln(w, "sources:")
			for _, src := range answer.Sources {
				label := src.Title
				if label == "" {
					label = src.URL
				}
				fmt.Fprintf(w, "  - %s  %s  %s\n", src.Kind, label, src.URL)
			}
		}
		if len(answer.Citations) > 0 {
			fmt.Fprintf(w, "citations: %d excerpt(s)\n", len(answer.Citations))
			for _, c := range answer.Citations {
				loc := c.Title
				if c.Section != "" {
					loc = strings.TrimSpace(loc + " / " + c.Section)
				}
				fmt.Fprintf(w, "  - %s  %s\n", loc, c.SourceURL)
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}
