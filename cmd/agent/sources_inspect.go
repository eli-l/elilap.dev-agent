package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/sources/web"
)

// inspectTimeout bounds the authoring work: the requested page plus the
// sitemap/feed enumeration the report needs. The provider skips the crawl for
// inspect, so this never walks the site.
const inspectTimeout = 45 * time.Second

// runSourcesInspect reports how the web provider would extract one page: the
// candidate content regions with usable CSS selectors, the page metadata, what
// discovery returns, and a suggested options block.
//
// It is the deterministic half of the "let the model pick the strategy" loop:
// the report is stable JSON/text, an LLM (or a human) reads it, and the
// outcome is either a selectors block in sources.yaml or - when no selector
// can express the page - a new web.Adapter in Go. Ingest itself never calls a
// model: the extraction a run uses is always the one in configuration.
func runSourcesInspect(args []string, stdout, stderr io.Writer) int {
	var (
		targetURL string
		sourceID  string
		format    string
	)
	cfg, _, code, ok := parseConfigFlag("inspect", args, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&targetURL, "url", "", "page URL to inspect (required)")
		fs.StringVar(&sourceID, "source", "", "use this configured web source's options")
		fs.StringVar(&format, "format", "", "output format: text (default) or json")
	})
	if !ok {
		return code
	}
	if strings.TrimSpace(targetURL) == "" {
		fmt.Fprintln(stderr, "agent sources inspect: --url is required")
		return 2
	}
	u, err := url.Parse(targetURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		// A scheme-relative URL ("//host/x") has a Host but no scheme; the
		// ad-hoc fallback would build "://host" and fail with an options error
		// that says nothing about the user's input.
		fmt.Fprintf(stderr, "agent sources inspect: --url %q is not an absolute http(s) URL\n", targetURL)
		return 2
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format != "" && format != "text" && format != "json" {
		fmt.Fprintf(stderr, "agent sources inspect: unknown --format %q (expected text or json)\n", format)
		return 2
	}

	opts, srcLabel, err := inspectOptions(cfg, sourceID, u)
	if err != nil {
		fmt.Fprintf(stderr, "agent sources inspect: %v\n", err)
		return 1
	}

	provider, err := web.NewProvider(opts, shared.NewHTTPClient(shared.DefaultHTTPTimeout), cfg.Defaults.UserAgent, nil)
	if err != nil {
		fmt.Fprintf(stderr, "agent sources inspect: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	analysis, err := provider.Inspect(ctx, targetURL)
	if err != nil {
		fmt.Fprintf(stderr, "agent sources inspect: %v\n", err)
		return 1
	}

	switch format {
	case "", "text":
		label := srcLabel
		if label == "" {
			label = "(ad-hoc options: no configured source matches this host)"
		}
		fmt.Fprintf(stdout, "source: %s\nstrategy configured: %s\n\n", label, opts.Strategy)
		fmt.Fprint(stdout, analysis.Text(provider.SiteURL().String()))
	case "json":
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(analysis); err != nil {
			fmt.Fprintf(stderr, "agent sources inspect: encode report: %v\n", err)
			return 1
		}
	}

	log := logging.Sources().With().
		Str("provider", "web").
		Str("url", analysis.URL).
		Int("candidates", len(analysis.Candidates)).
		Str("suggested", analysis.Suggested.Strategy).
		Logger()
	log.Debug().Msg("inspection complete")
	return 0
}

// inspectOptions resolves the options to inspect with: the named source, the
// first web source whose host matches the URL, or defaults keyed to the URL's
// origin. The second return value labels the choice for the report.
func inspectOptions(cfg *config.Config, sourceID string, target *url.URL) (*web.Options, string, error) {
	reg, err := newRegistry()
	if err != nil {
		return nil, "", err
	}
	pick := func(src config.Source) (*web.Options, error) {
		if src.Type != "web" {
			return nil, fmt.Errorf("source %q is type %q, not web", src.ID, src.Type)
		}
		parsed, err := reg.ParseOptions(src)
		if err != nil {
			return nil, err
		}
		opts, ok := parsed.(*web.Options)
		if !ok {
			return nil, fmt.Errorf("source %q: options are %T, want *web.Options", src.ID, parsed)
		}
		return opts, nil
	}

	if sourceID != "" {
		for _, src := range cfg.Sources {
			if src.ID == sourceID {
				opts, err := pick(src)
				return opts, "configured source " + src.ID, err
			}
		}
		return nil, "", fmt.Errorf("unknown source %q", sourceID)
	}

	for _, src := range cfg.Sources {
		if src.Type != "web" {
			continue
		}
		// A disabled source still owns its options: inspecting a page before
		// enabling the source is the normal authoring order. A source with
		// invalid options must not block inspecting a different host; its own
		// error is what `agent sources validate` reports.
		opts, err := pick(src)
		if err != nil {
			log := logging.Sources().With().Str("source", src.ID).Logger()
			log.Warn().Err(err).Msg("skipping source with invalid options")
			continue
		}
		if !opts.MatchesHost(target) {
			continue
		}
		label := "configured source " + src.ID
		if !src.Enabled {
			label += " (disabled)"
		}
		return opts, label, nil
	}

	opts, err := web.DefaultOptions(target.Scheme + "://" + target.Host)
	return opts, "", err
}
