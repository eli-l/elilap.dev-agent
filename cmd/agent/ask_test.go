package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/agentloop"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
)

func runAskTest(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runAsk(args, envcfg.Config{
		DBPath:       filepath.Join(t.TempDir(), "unused.db"),
		EmbedBaseURL: envcfg.DefaultModelBaseURL, ChatBaseURL: envcfg.DefaultModelBaseURL,
		EmbedModel: envcfg.DefaultEmbedModel,
		ChatModel:  envcfg.DefaultChatModel,
	}, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestRunAskRejectsUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no question", args: nil, want: "provide a question"},
		{name: "format", args: []string{"--format", "yaml", "q"}, want: "--format must be text or json"},
		{name: "iterations", args: []string{"--max-iterations", "0", "q"}, want: "budgets must be positive"},
		{name: "excerpts", args: []string{"--excerpt-tokens", "-1", "q"}, want: "budgets must be positive"},
		{name: "max tokens", args: []string{"--max-tokens", "0", "q"}, want: "budgets must be positive"},
		{name: "timeout", args: []string{"--timeout", "0s", "q"}, want: "--timeout must be positive"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runAskTest(t, tc.args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestRunAskRequiresIndex(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "missing.db")
	var stdout, stderr bytes.Buffer
	code := runAsk([]string{"--db", dbPath, "q"}, envcfg.Config{
		DBPath: dbPath, EmbedBaseURL: envcfg.DefaultModelBaseURL, ChatBaseURL: envcfg.DefaultModelBaseURL,
		EmbedModel: envcfg.DefaultEmbedModel, ChatModel: envcfg.DefaultChatModel,
	}, &stdout, &stderr)
	if code != 1 {
		t.Errorf("exit = %d, want 1 for a missing index", code)
	}
	if !strings.Contains(stderr.String(), "no index at") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func fixtureAnswer() agentloop.Answer {
	return agentloop.Answer{
		Answer: "Harnesses are a form of vendor lock-in.", ToolIterations: 2, ElapsedMS: 1234,
		Citations: []agentloop.Citation{
			{SourceKind: "ghost_post", SourceURL: "https://x/1", Title: "One", Excerpt: "first excerpt"},
			{SourceKind: "web_page", SourceURL: "https://x/2", Title: "Two", Section: "Body", Excerpt: "second excerpt"},
		},
		Sources: []agentloop.Source{
			{Kind: "ghost_post", URL: "https://x/1", Title: "One", Author: "Eli"},
			{Kind: "web_page", URL: "https://x/2", Title: "Two"},
		},
	}
}

func TestPrintAnswerJSON(t *testing.T) {
	want := fixtureAnswer()
	var buf bytes.Buffer
	if err := printAnswer(&buf, "json", want); err != nil {
		t.Fatalf("json: %v", err)
	}
	var got agentloop.Answer
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestPrintAnswerText(t *testing.T) {
	var buf bytes.Buffer
	if err := printAnswer(&buf, "text", fixtureAnswer()); err != nil {
		t.Fatalf("text: %v", err)
	}
	for _, want := range []string{
		"answer: Harnesses are a form of vendor lock-in.",
		"refused: false", "tool iterations: 2", "elapsed: 1234ms",
		"sources:", "ghost_post  One  https://x/1", "web_page  Two  https://x/2",
		"citations: 2 excerpt(s)", "One  https://x/1", "Two / Body  https://x/2",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, buf.String())
		}
	}
	// The CLI text view is a response surface too: no chunk id, ever.
	if strings.Contains(buf.String(), "chunk_id") {
		t.Errorf("text output exposes a chunk id:\n%s", buf.String())
	}
}

// TestAskJSONMatchesHandler proves the CLI and HTTP render the same answer
// struct identically.
func TestAskJSONMatchesHandler(t *testing.T) {
	want := fixtureAnswer()
	var cli bytes.Buffer
	if err := printAnswer(&cli, "json", want); err != nil {
		t.Fatalf("json: %v", err)
	}
	rec := postAsk(t, newAskHandler(&stubAnswerer{answer: want}), `{"question":"q"}`)
	var fromCLI, fromHTTP agentloop.Answer
	if err := json.Unmarshal(cli.Bytes(), &fromCLI); err != nil {
		t.Fatalf("decode cli: %v", err)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fromHTTP); err != nil {
		t.Fatalf("decode http: %v", err)
	}
	if !reflect.DeepEqual(fromCLI, fromHTTP) {
		t.Errorf("CLI = %+v\nHTTP = %+v", fromCLI, fromHTTP)
	}
}
