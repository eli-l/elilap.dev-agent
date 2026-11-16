package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/a2a"
	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
	"github.com/eli-l/elilap.dev-agent/internal/llm"
	"github.com/eli-l/elilap.dev-agent/internal/notify"
	"github.com/eli-l/elilap.dev-agent/internal/requests"
	"github.com/eli-l/elilap.dev-agent/internal/retrieval"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
	"github.com/eli-l/elilap.dev-agent/internal/store"
)

// requestRowWidth is where the inbox table truncates a question so one line
// stays readable in a terminal.
const requestRowWidth = 78

// runRequests implements `agent requests`: the owner-side inbox for deferred
// knowledge requests. It is the only surface that can read the stored question
// text; peers see only the task id they were given.
func runRequests(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		requestsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return runRequestsList(args[1:], rtCfg, stdout, stderr)
	case "show":
		return runRequestsShow(args[1:], rtCfg, stdout, stderr)
	case "review":
		return runRequestsTriage(args[1:], "review", rtCfg, stdout, stderr)
	case "resolve":
		return runRequestsResolve(args[1:], rtCfg, stdout, stderr)
	case "decline":
		return runRequestsTriage(args[1:], "decline", rtCfg, stdout, stderr)
	case "delete":
		return runRequestsDelete(args[1:], rtCfg, stdout, stderr)
	case "purge":
		return runRequestsPurge(args[1:], rtCfg, stdout, stderr)
	case "help", "-h", "--help":
		requestsUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "agent requests: unknown subcommand %q\n\n", args[0])
		requestsUsage(stderr)
		return 2
	}
}

// takeRequestID pulls the request id out of the argument list, because a Go
// FlagSet stops parsing at the first non-flag argument: both
// `requests resolve <id> --note x` and `requests resolve --note x <id>` work.
// Production ids always start with "req_"; the positional fallback covers a
// hand-typed id and the tests.
func takeRequestID(args []string) (string, []string) {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "req_") {
			continue
		}
		// A value that follows a *value-taking* flag (--note req_…, --answer
		// "req_… is …") belongs to that flag, not to the positional id. A
		// boolean flag (--no-ask, --json) does not consume the next argument,
		// so `resolve --no-ask <id>` still finds the id.
		if i > 0 && consumesValue(args[i-1]) {
			continue
		}
		rest := make([]string, 0, len(args)-1)
		rest = append(rest, args[:i]...)
		rest = append(rest, args[i+1:]...)
		return arg, rest
	}
	return "", args
}

// consumesValue reports whether a bare flag argument takes the next argument
// as its value. Boolean flags are deliberately absent: treating them as
// value-taking would let `--no-ask <id>` swallow the id.
func consumesValue(arg string) bool {
	if !strings.HasPrefix(arg, "-") || strings.Contains(arg, "=") {
		return false
	}
	return requestValueFlags[strings.TrimLeft(arg, "-")]
}

// requestValueFlags names the flags that consume the following argument.
var requestValueFlags = map[string]bool{
	"db":              true,
	"index":           true,
	"note":            true,
	"answer":          true,
	"source-url":      true,
	"status":          true,
	"limit":           true,
	"older-than":      true,
	"timeout":         true,
	"push-timeout":    true,
	"push-allow-host": true,
}

// positionalID resolves the id from the pre-scan or the single leftover
// argument. It returns "" when the command line is ambiguous.
func positionalID(id string, fs *flag.FlagSet) string {
	if id != "" {
		if fs.NArg() > 0 {
			return ""
		}
		return id
	}
	if fs.NArg() == 1 {
		return fs.Arg(0)
	}
	return ""
}

// openRequests opens the durable store for a triage command. Every command
// operates on a request that must already exist, so a missing store is a
// wrong-path error, never a new empty database: `delete` on a typo'd --db must
// not report success, and `list`/`show` must not create a second inbox beside
// the real one. A read-only command takes a read-only handle; the rest take a
// write handle on the existing file.
func openRequests(path string, stderr io.Writer, command string, readOnly bool) (*requests.Store, int) {
	var (
		store *requests.Store
		err   error
	)
	if readOnly {
		store, err = requests.OpenReadOnly(path)
	} else {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			err = fmt.Errorf("%w: %s", requests.ErrNoStore, path)
		} else {
			store, err = requests.Open(path)
		}
	}
	if errors.Is(err, requests.ErrNoStore) {
		fmt.Fprintf(stderr, "%s: no knowledge-request store at %s (pass --db, or start the server once with this index)\n", command, path)
		return nil, 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return nil, 1
	}
	return store, 0
}

func runRequestsList(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent requests list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	status := fs.String("status", "", "only this status: new, reviewed, resolved, declined, canceled")
	limit := fs.Int("limit", 50, "maximum rows")
	oldest := fs.Bool("oldest", false, "oldest first")
	asJSON := fs.Bool("json", false, "print JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent requests list: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, "agent requests list", true)
	if store == nil {
		return code
	}
	defer store.Close()

	rows, err := store.List(requests.ListFilter{Status: *status, Limit: *limit, OldestFirst: *oldest})
	if err != nil {
		fmt.Fprintf(stderr, "agent requests list: %v\n", err)
		return 1
	}
	pending, err := store.Pending()
	if err != nil {
		fmt.Fprintf(stderr, "agent requests list: %v\n", err)
		return 1
	}
	if *asJSON {
		payload := map[string]any{"pending": pending, "requests": rows}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintf(stderr, "agent requests list: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "no knowledge requests (pending: %d)\n", pending)
		return 0
	}
	fmt.Fprintf(stdout, "%-28s %-9s %-20s %-18s %s\n", "ID", "STATUS", "SUBMITTED", "PRINCIPAL", "QUESTION")
	for _, req := range rows {
		fmt.Fprintf(stdout, "%-28s %-9s %-20s %-18s %s\n",
			req.ID, req.Status, req.SubmittedAt, truncateCell(singleLine(printable(req.Principal)), 18),
			truncateCell(singleLine(printable(req.Question)), requestRowWidth))
	}
	fmt.Fprintf(stdout, "\n%d shown, %d pending\n", len(rows), pending)
	return 0
}

func runRequestsShow(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent requests show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	asJSON := fs.Bool("json", false, "print JSON instead of the detail view")
	id, rest := takeRequestID(args)
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	id = positionalID(id, fs)
	if id == "" {
		fmt.Fprintln(stderr, "agent requests show: exactly one request id is required")
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, "agent requests show", true)
	if store == nil {
		return code
	}
	defer store.Close()
	req, err := store.Get(id)
	if errors.Is(err, requests.ErrNotFound) {
		fmt.Fprintf(stderr, "agent requests show: no request %q\n", id)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent requests show: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(req); err != nil {
			fmt.Fprintf(stderr, "agent requests show: %v\n", err)
			return 1
		}
		return 0
	}
	printRequest(stdout, req)
	return 0
}

func runRequestsTriage(args []string, action string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	command := "agent requests " + action
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	note := fs.String("note", "", "owner note (private; never corpus evidence)")
	pushInsecure := fs.Bool("push-allow-insecure", false, "allow an http push target (loopback testing only)")
	var pushHosts hostList
	fs.Var(&pushHosts, "push-allow-host", "restrict peer push targets to this host (repeatable; .suffix matches a subtree)")
	timeout := fs.Duration("push-timeout", 10*time.Second, "delivery budget per push notification")
	id, rest := takeRequestID(args)
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	id = positionalID(id, fs)
	if id == "" {
		fmt.Fprintf(stderr, "%s: exactly one request id is required\n", command)
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, command, false)
	if store == nil {
		return code
	}
	defer store.Close()

	var (
		req requests.Request
		err error
	)
	switch action {
	case "review":
		req, err = store.Review(id, *note, nil)
	default:
		req, err = store.Decline(id, *note, nil)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", command, err)
		return 1
	}
	printRequest(stdout, req)
	deliverPeerNotification(store, req, *pushInsecure, pushHosts, *timeout, stdout, stderr, command)
	return 0
}

func runRequestsResolve(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent requests resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	indexFlag := fs.String("index", rtCfg.DBPath, "read-only SQLite index used for the grounded answer")
	sourceURL := fs.String("source-url", "", "the published page that now answers the question")
	note := fs.String("note", "", "owner note (private; never corpus evidence)")
	noAsk := fs.Bool("no-ask", false, "record a manual resolution without running the grounded loop")
	manualAnswer := fs.String("answer", "", "answer text for --no-ask")
	timeout := fs.Duration("timeout", askTimeout, "overall answer timeout")
	pushInsecure := fs.Bool("push-allow-insecure", false, "allow an http push target (loopback testing only)")
	var pushHosts hostList
	fs.Var(&pushHosts, "push-allow-host", "restrict peer push targets to this host (repeatable; .suffix matches a subtree)")
	pushTimeout := fs.Duration("push-timeout", 10*time.Second, "delivery budget per push notification")
	id, rest := takeRequestID(args)
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	id = positionalID(id, fs)
	if id == "" {
		fmt.Fprintln(stderr, "agent requests resolve: exactly one request id is required")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "agent requests resolve: --timeout must be positive")
		return 2
	}
	if *pushTimeout <= 0 {
		fmt.Fprintln(stderr, "agent requests resolve: --push-timeout must be positive")
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, "agent requests resolve", false)
	if store == nil {
		return code
	}
	defer store.Close()

	req, err := store.Get(id)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return 1
	}
	// A closed request cannot be resolved again, and the grounded loop below
	// costs a real model call. Reject the state now instead of after paying
	// for an answer that could never be recorded.
	if !req.Open() {
		fmt.Fprintf(stderr, "agent requests resolve: request %s is %s, not open\n", req.ID, req.Status)
		return 1
	}

	resolution := requests.Resolution{SourceURL: *sourceURL, Note: *note}
	if *noAsk {
		if strings.TrimSpace(*manualAnswer) == "" {
			fmt.Fprintln(stderr, "agent requests resolve: --no-ask needs --answer (the grounded path is the default)")
			return 2
		}
		resolution.Answer = strings.TrimSpace(*manualAnswer)
	} else {
		answer, code := answerForRequest(rtCfg, *indexFlag, req.Question, *timeout, stderr)
		if code != 0 {
			return code
		}
		if answer.Refused {
			fmt.Fprintf(stderr, "agent requests resolve: the corpus still does not answer this request (%q).\n"+
				"publish about it and re-run ingest + embed, or pass --no-ask --answer with what to record.\n",
				agentloop.Refusal)
			return 1
		}
		resolution.Answer = answer.Answer
		citations, err := json.Marshal(answer.Citations)
		if err != nil {
			fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
			return 1
		}
		sources, err := json.Marshal(answer.Sources)
		if err != nil {
			fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
			return 1
		}
		resolution.Citations = string(citations)
		resolution.Sources = string(sources)
	}

	resolved, err := store.Resolve(req.ID, resolution)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return 1
	}
	printRequest(stdout, resolved)
	deliverPeerNotification(store, resolved, *pushInsecure, pushHosts, *pushTimeout, stdout, stderr, "agent requests resolve")
	return 0
}

// answerForRequest runs the grounded loop on the stored question, exactly as
// `agent ask` would. A resolution is only honest when the corpus now grounds
// it, so the refusal is surfaced instead of stored.
func answerForRequest(rtCfg envcfg.Config, dbPath, question string, timeout time.Duration, stderr io.Writer) (agentloop.Answer, int) {
	st, err := store.OpenReadOnly(dbPath)
	if errors.Is(err, store.ErrNoDatabase) {
		fmt.Fprintf(stderr, "agent requests resolve: no index at %s; run `agent ingest` and `agent embed` first\n", dbPath)
		return agentloop.Answer{}, 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return agentloop.Answer{}, 1
	}
	defer st.Close()
	if err := verifyIndexModel(st, rtCfg.EmbedModel, int(rtCfg.EmbedDims)); err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return agentloop.Answer{}, 1
	}
	hc := shared.NewHTTPClient(timeout)
	embedClient, err := llm.NewClient(rtCfg.EmbedBaseURL, rtCfg.EmbedAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return agentloop.Answer{}, 1
	}
	chatClient, err := llm.NewClient(rtCfg.ChatBaseURL, rtCfg.ChatAPIKey, hc)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return agentloop.Answer{}, 1
	}
	chatClient.TokenParam = rtCfg.ChatTokenParam
	runner := &agentloop.Runner{
		Searcher:   &retrieval.Searcher{Store: st, Embedder: embedClient, Model: rtCfg.EmbedModel},
		Chat:       chatClient,
		Model:      rtCfg.ChatModel,
		MaxElapsed: timeout,
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	answer, err := runner.Run(ctx, question)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests resolve: %v\n", err)
		return agentloop.Answer{}, 1
	}
	return answer, 0
}

func runRequestsDelete(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent requests delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	id, rest := takeRequestID(args)
	if err := fs.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	id = positionalID(id, fs)
	if id == "" {
		fmt.Fprintln(stderr, "agent requests delete: exactly one request id is required")
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, "agent requests delete", false)
	if store == nil {
		return code
	}
	defer store.Close()
	if err := store.Delete(id); err != nil {
		fmt.Fprintf(stderr, "agent requests delete: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "deleted %s\n", id)
	return 0
}

func runRequestsPurge(args []string, rtCfg envcfg.Config, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agent requests purge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbFlag := fs.String("db", rtCfg.RequestsPath(), "path to the knowledge-request store")
	olderThan := fs.String("older-than", "", "delete closed requests submitted before this age, e.g. 180d or 4320h (required)")
	status := fs.String("status", "", "only this closed status (default: resolved, declined, canceled)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agent requests purge: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	age, err := parseAge(*olderThan)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests purge: %v\n", err)
		return 2
	}
	store, code := openRequests(*dbFlag, stderr, "agent requests purge", false)
	if store == nil {
		return code
	}
	defer store.Close()
	var statuses []string
	if *status != "" {
		statuses = []string{*status}
	}
	deleted, err := store.Purge(time.Now().Add(-age), statuses)
	if err != nil {
		fmt.Fprintf(stderr, "agent requests purge: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "purged %d request(s)\n", deleted)
	return 0
}

// deliverPeerNotification pushes the resolved (or declined) task to the peer's
// registered webhook. A delivery failure is reported but never undoes the
// recorded outcome: `tasks/get` remains the durable path.
func deliverPeerNotification(store *requests.Store, req requests.Request, insecure bool, hosts hostList, timeout time.Duration, stdout, stderr io.Writer, command string) {
	if req.PushURL == "" {
		fmt.Fprintln(stdout, "peer notification: none registered (the peer can poll tasks/get)")
		return
	}
	sender := notify.New(notify.Policy{
		AllowInsecure: insecure,
		AllowHosts:    hosts,
		// The CLI waits for the answer, so one attempt per address is enough
		// for an operator watching the command; the sender still retries
		// transient failures within the timeout.
		MaxAttempts:    3,
		AttemptTimeout: timeout,
	})
	task := a2a.TaskForRequest(req)
	ctx, cancel := context.WithTimeout(context.Background(), timeout*4)
	defer cancel()
	attempts, err := sender.Send(ctx, req.PushURL, req.PushToken, task)
	if recordErr := store.RecordPush(req.ID, attempts, err); recordErr != nil {
		fmt.Fprintf(stderr, "%s: record push delivery: %v\n", command, recordErr)
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: peer notification failed after %d attempt(s): %v\n", command, attempts, err)
		fmt.Fprintln(stderr, "the peer can still poll tasks/get for this request id")
		return
	}
	fmt.Fprintf(stdout, "peer notification: delivered in %d attempt(s)\n", attempts)
}

// printRequest writes the human detail view. Every peer-supplied field goes
// through printable first: question, reason, context, note, source URL, answer,
// and push state are stored verbatim, and a raw ESC/OSC sequence or carriage
// return could forge lines or drive the operator's terminal.
func printRequest(w io.Writer, req requests.Request) {
	fmt.Fprintf(w, "id:          %s\n", req.ID)
	fmt.Fprintf(w, "status:      %s\n", req.Status)
	fmt.Fprintf(w, "submitted:   %s\n", req.SubmittedAt)
	fmt.Fprintf(w, "principal:   %s\n", printable(req.Principal))
	if req.Origin != "" {
		fmt.Fprintf(w, "origin:      %s\n", printable(req.Origin))
	}
	fmt.Fprintf(w, "question:    %s\n", printable(req.Question))
	if req.Reason != "" {
		fmt.Fprintf(w, "reason:      %s\n", printable(req.Reason))
	}
	if req.Context != "" {
		fmt.Fprintf(w, "context:\n%s\n", indent(printable(req.Context), "  "))
	}
	if req.Note != "" {
		fmt.Fprintf(w, "note:        %s\n", printable(req.Note))
	}
	if req.SourceURL != "" {
		fmt.Fprintf(w, "source:      %s\n", printable(req.SourceURL))
	}
	if req.Answer != "" {
		fmt.Fprintf(w, "answer:\n%s\n", indent(printable(req.Answer), "  "))
	}
	if req.PushURL != "" {
		fmt.Fprintf(w, "push:        %s (%d attempt(s)", printable(req.PushURL), req.PushAttempts)
		if req.PushDelivered != "" {
			fmt.Fprintf(w, ", delivered %s", req.PushDelivered)
		} else if req.PushLastError != "" {
			fmt.Fprintf(w, ", last error: %s", printable(req.PushLastError))
		}
		fmt.Fprintln(w, ")")
	}
}

// hostList collects a repeatable --push-allow-host flag.
type hostList []string

func (h *hostList) String() string { return strings.Join(*h, ",") }

func (h *hostList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("host must not be empty")
	}
	*h = append(*h, value)
	return nil
}

// parseAge accepts a Go duration or a day count (180d), because a retention
// window reads better in days than in hours.
func parseAge(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("--older-than is required, for example --older-than 180d")
	}
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("--older-than %q is not a positive day count", raw)
		}
		// An overflowing day count wraps negative, which would put the cut-off
		// in the future and purge every closed request.
		if int64(days) > math.MaxInt64/int64(24*time.Hour) {
			return 0, fmt.Errorf("--older-than %q is out of range", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	age, err := time.ParseDuration(raw)
	if err != nil || age <= 0 {
		return 0, fmt.Errorf("--older-than %q is not a positive duration", raw)
	}
	return age, nil
}

// printable strips control and escape characters (C0, DEL, C1) from
// peer-supplied text while keeping deliberate newlines, so a stored question
// cannot rewrite the operator's terminal or forge neighbouring lines.
func printable(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteByte(' ')
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// drop control, escape, and C1 bytes
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// singleLine collapses whitespace so a multi-line question stays one row.
func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateCell shortens a table cell to width with an ellipsis. It counts
// runes, so a non-ASCII question is shortened at a character boundary instead
// of splitting a UTF-8 sequence into invalid output.
func truncateCell(s string, width int) string {
	if width <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width < 2 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

// indent prefixes every line, so a stored multi-line body is readable.
func indent(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func requestsUsage(w io.Writer) {
	fmt.Fprint(w, `usage: agent requests <command> [flags]

The owner inbox for deferred knowledge requests. Question text is visible
here only; peers see the task id they were given.

Commands:
  list      list requests (--status, --limit, --oldest, --json)
  show      show one request and its stored answer (--json)
  review    mark a request as being worked on (--note)
  resolve   record the grounded answer (the default) and notify the peer
            (--source-url; --no-ask --answer overrides)
  decline   close a request without an answer (--note)
  delete    remove one request
  purge     delete old closed requests (--older-than 180d, --status)

Every command accepts --db (the request store), and resolve also accepts
--index (the writing index used by --ask). Both follow AGENT_REQUESTS_DB_PATH
and AGENT_DB_PATH; if the server was started with explicit --db/--requests-db
flags, pass the same paths here or you are looking at a different store.

The store lives beside the index (default requests.db) and is never rebuilt
by re-ingesting. Peer push targets must be https unless --push-allow-insecure
is given for loopback testing.
`)
}
