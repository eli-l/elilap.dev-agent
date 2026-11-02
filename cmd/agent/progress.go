package main

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/embed"
)

// progressInterval is how often an idle batch redraws the progress line, so
// the elapsed timer keeps moving even while one embeddings request is slow.
const progressInterval = time.Second

// progressPrinter renders a single, self-overwriting progress line on stderr.
// It is deliberately stderr-only: stdout stays reserved for the command's
// result, so `agent embed --progress | jq` still parses.
//
// The embed runner reports on its own goroutine; a ticker goroutine redraws
// between batches. A mutex serializes the two, and the line is padded with
// spaces so a shorter update cannot leave tails of the previous line behind.
type progressPrinter struct {
	w io.Writer

	mu       sync.Mutex
	total    int
	done     int
	inFlight int
	start    time.Time
	lineLen  int

	ticker  *time.Ticker
	stopped chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
}

// newProgressPrinter starts the redraw ticker. Call stop when the run ends.
func newProgressPrinter(w io.Writer) *progressPrinter {
	p := &progressPrinter{
		w:       w,
		start:   time.Now(),
		ticker:  time.NewTicker(progressInterval),
		stopped: make(chan struct{}),
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			select {
			case <-p.stopped:
				return
			case <-p.ticker.C:
				p.render(false)
			}
		}
	}()
	return p
}

// update records the latest runner observation and redraws immediately.
func (p *progressPrinter) update(pr embed.Progress) {
	p.mu.Lock()
	p.total = pr.Total
	p.done = pr.Done
	p.inFlight = pr.InFlight
	p.mu.Unlock()
	p.render(false)
}

// stop ends the ticker and finishes the line so the summary that follows
// starts cleanly. When a line was already drawn, it only adds the newline:
// re-rendering here would print a duplicate "done" line just for a tenth of
// a second of extra elapsed time.
func (p *progressPrinter) stop() {
	p.once.Do(func() {
		close(p.stopped)
		p.ticker.Stop()
	})
	p.wg.Wait()
	p.mu.Lock()
	drawn := p.lineLen > 0
	if drawn {
		fmt.Fprint(p.w, "\n")
		p.lineLen = 0
	}
	p.mu.Unlock()
	if !drawn {
		p.render(true)
	}
}

// render draws the current state. final appends a newline and clears the
// padding bookkeeping.
func (p *progressPrinter) render(final bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	line := formatProgress(p.total, p.done, p.inFlight, time.Since(p.start))
	pad := 0
	if p.lineLen > len(line) {
		pad = p.lineLen - len(line)
	}
	p.lineLen = len(line)

	// The lock is held across the write: the ticker and the runner both call
	// render, and an out-of-order write would leave stale text on the line.
	// A failed write to stderr is not actionable here.
	fmt.Fprintf(p.w, "\r%s%s", line, strings.Repeat(" ", pad))
	if final {
		fmt.Fprint(p.w, "\n")
		p.lineLen = 0
	}
}

// formatProgress is the one definition of the progress line, kept pure so it
// can be tested without a terminal or a running embedder.
func formatProgress(total, done, inFlight int, elapsed time.Duration) string {
	if total <= 0 {
		return "embedding: nothing to do"
	}
	rate := 0.0
	if secs := elapsed.Seconds(); secs > 0 {
		rate = float64(done) / secs
	}
	if done >= total && inFlight == 0 {
		line := fmt.Sprintf("embedding: done %d/%d chunks in %s", done, total, progressDuration(elapsed))
		if rate > 0 {
			line += fmt.Sprintf(" (%.1f chunks/s)", rate)
		}
		return line
	}

	parts := []string{fmt.Sprintf("embedding: %d/%d chunks (%d%%)", done, total, done*100/total)}
	if inFlight > 0 {
		parts = append(parts, fmt.Sprintf("%d in flight", inFlight))
	}
	parts = append(parts, "elapsed "+progressDuration(elapsed))
	if rate > 0 {
		parts = append(parts, fmt.Sprintf("%.1f chunks/s", rate))
		if done < total {
			eta := time.Duration(float64(total-done) / rate * float64(time.Second))
			parts = append(parts, "eta "+progressDuration(eta))
		}
	}
	return strings.Join(parts, " | ")
}

// progressDuration formats elapsed and ETA values compactly: tenths under a
// second, one decimal under a minute, and minutes otherwise.
func progressDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
