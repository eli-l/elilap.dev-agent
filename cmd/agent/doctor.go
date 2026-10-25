package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// Defaults for the runtime environment the doctor reports on. They mirror
// PLAN.md Decisions 3, 10 and 11: every one of them is overridable through
// the environment, so the doctor reports the resolved value, never a
// hardcoded one.
const (
	defaultModelBaseURL = "http://localhost:8080/v1"
	defaultEmbedModel   = "embeddinggemma-300m"
	defaultChatModel    = "spark-x2.5-4b"
	defaultDBPath       = "index.db"
)

// checkState is the outcome of one doctor check.
type checkState int

const (
	statePass checkState = iota
	stateWarn
	stateFail
)

func (s checkState) String() string {
	switch s {
	case statePass:
		return "ok"
	case stateWarn:
		return "warn"
	default:
		return "fail"
	}
}

// doctorReport accumulates check outcomes so the summary can count them.
type doctorReport struct {
	passes int
	warns  int
	fails  int
}

// add records one check outcome and prints it.
func (r *doctorReport) add(state checkState, w io.Writer, format string, args ...any) {
	switch state {
	case statePass:
		r.passes++
	case stateWarn:
		r.warns++
	default:
		r.fails++
	}
	fmt.Fprintf(w, "%-4s %s\n", state, fmt.Sprintf(format, args...))
}

// runDoctor runs the preflight checks and returns the process exit code: 0
// when no check failed, 1 when at least one failed, 2 on a usage error.
//
// The doctor answers one question - "can this machine run the agent right
// now?" - and every failure it reports comes with the env var or file that
// fixes it. It never prints a credential value, only the name of the
// variable and whether it is set.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := defaultConfigPath
	fs.StringVar(&path, "config", defaultConfigPath, "path to sources.yaml")
	timeout := 5 * time.Second
	fs.DurationVar(&timeout, "timeout", timeout, "timeout for the model-server probe")
	skipNetwork := fs.Bool("skip-network", false, "skip the model-server probe (offline check)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent doctor: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if timeout <= 0 {
		fmt.Fprintln(stderr, "agent doctor: --timeout must be greater than zero")
		return 2
	}

	rep := &doctorReport{}
	fmt.Fprintln(stdout, "doctor: checking the runtime requirements for this machine")

	cfg, err := config.Load(path)
	if err != nil {
		rep.add(stateFail, stdout, "config: %v", err)
		fmt.Fprintf(stdout, "hint: %s could not be loaded - create it from the template in the repo root or pass --config <path>\n", path)
	} else {
		rep.add(statePass, stdout, "config: %s (version %d, %d source(s), %d enabled)",
			path, cfg.Version, len(cfg.Sources), len(cfg.Enabled()))
		checkProviders(rep, stdout, cfg)
		checkCredentials(rep, stdout, cfg)
	}
	checkModelServer(rep, stdout, timeout, *skipNetwork)
	checkIndexPath(rep, stdout)

	fmt.Fprintf(stdout, "\ndoctor: %d ok, %d warning(s), %d failure(s)\n", rep.passes, rep.warns, rep.fails)
	if rep.fails > 0 {
		fmt.Fprintln(stdout, "next: fix the failing checks above, then re-run \"agent doctor\"")
		return 1
	}
	return 0
}

// checkProviders validates every configured source's options against its
// provider schema. A bad option is a hard failure: it would break the next
// ingest run.
func checkProviders(rep *doctorReport, w io.Writer, cfg *config.Config) {
	reg, err := newRegistry()
	if err != nil {
		rep.add(stateFail, w, "providers: %v", err)
		return
	}
	if err := reg.ValidateAll(cfg); err != nil {
		rep.add(stateFail, w, "providers: %v", err)
		fmt.Fprintln(w, "hint: fix the options block of the named source in sources.yaml")
		return
	}
	rep.add(statePass, w, "providers: %d provider type(s) validated (%s)",
		len(reg.KnownTypes()), strings.Join(reg.KnownTypes(), ", "))
}

// checkCredentials reports credential environment variables that are not
// set. Values are never read or printed - only names and set state.
func checkCredentials(rep *doctorReport, w io.Writer, cfg *config.Config) {
	missing := cfg.MissingEnv()
	if len(missing) == 0 {
		rep.add(statePass, w, "credentials: every referenced environment variable is set")
		return
	}
	rep.add(stateWarn, w, "credentials: %d environment variable(s) not set: %s",
		len(missing), strings.Join(missing, ", "))
	fmt.Fprintln(w, "hint: export the variables above (see .env.example); credential values are never read from sources.yaml")
}

// checkModelServer probes the OpenAI-compatible endpoint the agent will use
// for embeddings and chat, and checks that the two configured models are in
// its catalog.
func checkModelServer(rep *doctorReport, w io.Writer, timeout time.Duration, skipNetwork bool) {
	base := envOr("LOCALAI_BASE_URL", defaultModelBaseURL)
	embedModel := envOr("EMBED_MODEL", defaultEmbedModel)
	chatModel := envOr("CHAT_MODEL", defaultChatModel)

	if skipNetwork {
		rep.add(stateWarn, w, "model server: skipped (--skip-network); would probe %s", base)
		return
	}

	ids, err := fetchModels(base, timeout)
	if err != nil {
		rep.add(stateFail, w, "model server: %s: %v", base, err)
		fmt.Fprintf(w, "hint: check LOCALAI_BASE_URL (currently %q) and that the server is running; the base URL must end in /v1. The contract is documented in docs/localai.md\n", base)
		return
	}
	rep.add(statePass, w, "model server: %s is reachable (%d model(s))", base, len(ids))

	for _, m := range []struct {
		role string
		id   string
	}{
		{"embed model", embedModel},
		{"chat model", chatModel},
	} {
		if contains(ids, m.id) {
			rep.add(statePass, w, "%s: %s is served", m.role, m.id)
			continue
		}
		rep.add(stateWarn, w, "%s: %s is not in the server's catalog (served: %s)",
			m.role, m.id, strings.Join(ids, ", "))
	}
}

// checkIndexPath reports where the index would live and whether that
// location is writable.
func checkIndexPath(rep *doctorReport, w io.Writer) {
	dbPath := envOr("AGENT_DB_PATH", defaultDBPath)
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		rep.add(stateFail, w, "index: cannot resolve %q: %v", dbPath, err)
		return
	}
	dir := filepath.Dir(abs)
	info, err := os.Stat(dir)
	if err != nil {
		rep.add(stateWarn, w, "index: %s does not exist yet (created on first ingest)", abs)
		return
	}
	if !info.IsDir() {
		rep.add(stateFail, w, "index: %s is not a directory", dir)
		return
	}
	probe, err := os.CreateTemp(dir, ".agent-doctor-*")
	if err != nil {
		rep.add(stateFail, w, "index: %s is not writable: %v", dir, err)
		fmt.Fprintf(w, "hint: set AGENT_DB_PATH (currently %q) to a path this user can write\n", dbPath)
		return
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)

	if fi, err := os.Stat(abs); err == nil {
		rep.add(statePass, w, "index: %s exists (%d bytes), directory is writable", abs, fi.Size())
		return
	}
	rep.add(statePass, w, "index: %s will be created here, directory is writable", abs)
}

// fetchModels asks an OpenAI-compatible server for its model catalog.
func fetchModels(base string, timeout time.Duration) ([]string, error) {
	url := strings.TrimRight(base, "/") + "/models"
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot be reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server answered %s for GET %s - the base URL is probably wrong (it must end in /v1)", resp.Status, url)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", url, err)
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}

// contains reports whether ids holds want.
func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// envOr returns the trimmed value of the environment variable name, or
// fallback when it is unset or empty.
func envOr(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}
