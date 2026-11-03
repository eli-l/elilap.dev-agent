// Command eval runs the Phase 2 retrieval eval: for each query in
// eval/queries.yaml it calls `agent query --format jsonl` and passes the query
// when at least one human-verified chunk id appears in the top results.
//
// It shells out to the real CLI on purpose: the eval grades the same hybrid
// engine and output path the tool surfaces use, not a test double.
//
//	go run -mod=vendor ./scripts/eval
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// queryTimeout bounds one subprocess invocation. The CLI's own --timeout
// bounds the search; this outer deadline also covers a hung startup or index
// open, so the eval cannot block forever.
const queryTimeout = 2 * time.Minute

// file is the eval artifact schema.
type file struct {
	Version       int     `yaml:"version"`
	TopN          int     `yaml:"top_n"`
	PassThreshold int     `yaml:"pass_threshold"`
	Queries       []query `yaml:"queries"`
}

type query struct {
	ID           string   `yaml:"id"`
	Query        string   `yaml:"query"`
	ExpectChunks []string `yaml:"expect_chunk_ids"`
	Note         string   `yaml:"note"`
}

// hit is the subset of a `agent query --format jsonl` line the eval reads.
type hit struct {
	ChunkID string `json:"chunk_id"`
	Title   string `json:"title"`
	Section string `json:"section"`
}

func main() {
	path := flag.String("file", "eval/queries.yaml", "eval artifact to run")
	agent := flag.String("agent", "./agent", "path to the agent binary")
	topOverride := flag.Int("top", 0, "override top_n for every query")
	flag.Parse()

	if err := run(*path, *agent, *topOverride, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		os.Exit(1)
	}
}

func run(path, agent string, topOverride int, out *os.File) error {
	if _, err := os.Stat(agent); err != nil {
		return fmt.Errorf("agent binary %q not found; build it with `go build -tags sqlite_fts5 -o agent ./cmd/agent`", agent)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if f.Version != 1 {
		return fmt.Errorf("%s: unsupported version %d, want 1", path, f.Version)
	}
	if len(f.Queries) == 0 {
		return fmt.Errorf("%s: no queries", path)
	}
	if f.TopN <= 0 {
		f.TopN = 5
	}
	if topOverride > 0 {
		f.TopN = topOverride
	}
	threshold := f.PassThreshold
	if threshold <= 0 {
		threshold = len(f.Queries) * 3 / 4
		// A one-query artifact would otherwise floor to 0 and always pass.
		if threshold < 1 {
			threshold = 1
		}
	}

	passed := 0
	for _, q := range f.Queries {
		hits, err := runQuery(agent, f.TopN, q.Query)
		if err != nil {
			return fmt.Errorf("query %q: %w", q.ID, err)
		}
		ok, _ := matched(hits, q.ExpectChunks)
		if ok {
			passed++
			fmt.Fprintf(out, "PASS  %-26s %s\n", q.ID, q.Query)
			continue
		}
		fmt.Fprintf(out, "FAIL  %-26s %s\n", q.ID, q.Query)
		if len(q.ExpectChunks) > 0 {
			fmt.Fprintf(out, "      expected one of: %s\n", strings.Join(shortIDs(q.ExpectChunks), ", "))
		}
		for i, h := range hits {
			fmt.Fprintf(out, "      %d. %s / %s (%s)\n", i+1, h.Title, h.Section, shortID(h.ChunkID))
		}
	}

	fmt.Fprintf(out, "\n%d/%d passed (threshold %d)\n", passed, len(f.Queries), threshold)
	if passed < threshold {
		return fmt.Errorf("%d/%d below the pass threshold of %d", passed, len(f.Queries), threshold)
	}
	return nil
}

// runQuery shells out to the CLI and parses its JSONL output.
func runQuery(agent string, topN int, text string) ([]hit, error) {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, agent, "query", "--format", "jsonl", "--top", strconv.Itoa(topN), text)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("timed out after %s", queryTimeout)
		}
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var hits []hit
	scanner := bufio.NewScanner(&stdout)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var h hit
		if err := json.Unmarshal([]byte(line), &h); err != nil {
			return nil, fmt.Errorf("decode hit: %w", err)
		}
		hits = append(hits, h)
	}
	return hits, scanner.Err()
}

// matched reports whether any expected id is among the hits.
func matched(hits []hit, expect []string) (bool, string) {
	want := make(map[string]bool, len(expect))
	for _, id := range expect {
		want[id] = true
	}
	for _, h := range hits {
		if want[h.ChunkID] {
			return true, h.ChunkID
		}
	}
	return false, ""
}

func shortIDs(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = shortID(id)
	}
	return out
}

func shortID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}
