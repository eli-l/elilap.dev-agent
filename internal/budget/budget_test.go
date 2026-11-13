package budget_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/budget"
)

// clock is a manually advanced UTC clock.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newClock() *clock {
	return &clock{now: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)}
}

func newLimiter(t *testing.T, bounds budget.Bounds, path string) (*budget.Limiter, *clock) {
	t.Helper()
	c := newClock()
	l, err := budget.New(budget.Config{Bounds: bounds, Path: path, Now: c.Now})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return l, c
}

func agent(id, addr string) budget.Caller {
	return budget.Caller{Principal: budget.Principal{Kind: budget.KindAgent, ID: id}, Address: addr}
}

func wantRefusal(t *testing.T, err error, want budget.Scope) *budget.Refusal {
	t.Helper()
	refusal, ok := budget.AsRefusal(err)
	if !ok {
		t.Fatalf("error = %v, want a *budget.Refusal", err)
	}
	if refusal.Scope != want {
		t.Fatalf("scope = %q, want %q", refusal.Scope, want)
	}
	return refusal
}

func TestMinuteRateAllowsTheBurstThenRefuses(t *testing.T) {
	l, _ := newLimiter(t, budget.Bounds{PerMinute: 2}, "")

	for i := 0; i < 2; i++ {
		release, err := l.Acquire(agent("peer", "203.0.113.9"))
		if err != nil {
			t.Fatalf("answer %d refused: %v", i+1, err)
		}
		release()
	}
	refusal := wantRefusal(t, acquireRefusal(t, l, agent("peer", "203.0.113.9")), budget.ScopeRate)
	if refusal.RetryAfter <= 0 || refusal.RetryAfter > time.Minute {
		t.Errorf("RetryAfter = %v, want a positive duration under a minute", refusal.RetryAfter)
	}
}

func TestIdentityDailyQuota(t *testing.T) {
	l, c := newLimiter(t, budget.Bounds{PerDay: 2}, "")

	for i := 0; i < 2; i++ {
		release, err := l.Acquire(agent("alice", "203.0.113.9"))
		if err != nil {
			t.Fatalf("answer %d refused: %v", i+1, err)
		}
		release()
	}
	refusal := wantRefusal(t, acquireRefusal(t, l, agent("alice", "203.0.113.9")), budget.ScopeIdentityDaily)
	// 12:00 UTC leaves twelve hours in the day.
	if refusal.RetryAfter != 12*time.Hour {
		t.Errorf("RetryAfter = %v, want the time to UTC midnight", refusal.RetryAfter)
	}

	// A different identity on the same address has its own share.
	release, err := l.Acquire(agent("bob", "203.0.113.9"))
	if err != nil {
		t.Fatalf("bob refused: %v", err)
	}
	release()

	// The next day resets.
	c.Advance(12 * time.Hour)
	release, err = l.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("alice refused after the UTC day rolled over: %v", err)
	}
	release()
}

// TestAddressDailyBackstopsRotatingIdentities is the security property of the
// public mode: an unauthenticated identity can be invented at will, so the
// per-address gate (not the per-identity one) has to stop a peer.
func TestAddressDailyBackstopsRotatingIdentities(t *testing.T) {
	l, _ := newLimiter(t, budget.Bounds{PerDay: 100, PerAddressPerDay: 2, GlobalPerDay: 100}, "")

	for i := 0; i < 2; i++ {
		release, err := l.Acquire(agent("rotating-"+string(rune('a'+i)), "203.0.113.9"))
		if err != nil {
			t.Fatalf("answer %d refused: %v", i+1, err)
		}
		release()
	}
	wantRefusal(t, acquireRefusal(t, l, agent("rotating-c", "203.0.113.9")), budget.ScopeAddressDaily)

	// Another address is unaffected.
	release, err := l.Acquire(agent("rotating-d", "198.51.100.7"))
	if err != nil {
		t.Fatalf("a different address was refused: %v", err)
	}
	release()
}

func TestGlobalDailyCapBoundsEverything(t *testing.T) {
	l, _ := newLimiter(t, budget.Bounds{PerDay: 100, PerAddressPerDay: 100, GlobalPerDay: 2}, "")

	for i := 0; i < 2; i++ {
		release, err := l.Acquire(agent("peer", "203.0.113."+string(rune('1'+i))))
		if err != nil {
			t.Fatalf("answer %d refused: %v", i+1, err)
		}
		release()
	}
	wantRefusal(t, acquireRefusal(t, l, agent("someone-else", "198.51.100.7")), budget.ScopeGlobalDaily)
}

func TestInflightCapRefusesInsteadOfQueueing(t *testing.T) {
	l, _ := newLimiter(t, budget.Bounds{MaxInflight: 1}, "")

	release, err := l.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("first answer refused: %v", err)
	}
	wantRefusal(t, acquireRefusal(t, l, agent("bob", "198.51.100.7")), budget.ScopeBusy)

	release()
	release() // idempotent: a second call must not free a slot twice.

	other, err := l.Acquire(agent("bob", "198.51.100.7"))
	if err != nil {
		t.Fatalf("answer refused after release: %v", err)
	}
	other()
	// The idempotent release above must not have added a second slot.
	a, err := l.Acquire(agent("carol", "192.0.2.1"))
	if err != nil {
		t.Fatalf("carol refused: %v", err)
	}
	defer a()
	wantRefusal(t, acquireRefusal(t, l, agent("dave", "192.0.2.2")), budget.ScopeBusy)
}

func TestRefusalDoesNotBurnDailyQuota(t *testing.T) {
	l, _ := newLimiter(t, budget.Bounds{PerDay: 1, PerAddressPerDay: 1}, "")

	release, err := l.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("first answer refused: %v", err)
	}
	release()
	// Both refusals come from the daily gates; neither may increment a counter.
	wantRefusal(t, acquireRefusal(t, l, agent("alice", "203.0.113.9")), budget.ScopeIdentityDaily)
	wantRefusal(t, acquireRefusal(t, l, agent("bob", "203.0.113.9")), budget.ScopeAddressDaily)

	if got := l.Snapshot().Global; got != 1 {
		t.Errorf("global counter = %d, want 1", got)
	}
}

func TestCountersPersistAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	l, _ := newLimiter(t, budget.Bounds{PerDay: 2}, path)

	for i := 0; i < 2; i++ {
		release, err := l.Acquire(agent("alice", "203.0.113.9"))
		if err != nil {
			t.Fatalf("answer %d refused: %v", i+1, err)
		}
		release()
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat counter file: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("counter file mode = %o, want 600", mode)
	}

	// A restart must not refund the day.
	again, err := budget.New(budget.Config{Bounds: budget.Bounds{PerDay: 2}, Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	wantRefusal(t, acquireRefusal(t, again, agent("alice", "203.0.113.9")), budget.ScopeIdentityDaily)
}

func TestYesterdayCountersDoNotCarryOver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	c := newClock()
	l, err := budget.New(budget.Config{Bounds: budget.Bounds{PerDay: 1}, Path: path, Now: c.Now})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	release, err := l.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("first answer refused: %v", err)
	}
	release()

	next := c.Now().Add(24 * time.Hour)
	reopened, err := budget.New(budget.Config{
		Bounds: budget.Bounds{PerDay: 1},
		Path:   path,
		Now:    func() time.Time { return next },
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	release, err = reopened.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("a new day was still charged yesterday's answers: %v", err)
	}
	release()
}

func TestCorruptCounterFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := budget.New(budget.Config{Bounds: budget.Bounds{PerDay: 1}, Path: path}); err == nil {
		t.Fatal("New() accepted a corrupt counter file; a silent reset would fail open")
	}
}

func TestNilLimiterAllowsEverything(t *testing.T) {
	var l *budget.Limiter
	release, err := l.Acquire(agent("alice", "203.0.113.9"))
	if err != nil {
		t.Fatalf("nil limiter refused: %v", err)
	}
	release()
}

func TestValidID(t *testing.T) {
	valid := []string{"alice", "peer-7", "a.b_c", "A1"}
	for _, id := range valid {
		if !budget.ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	invalid := []string{"", " ", "with space", "a:b", "x/y", "<script>", string(make([]byte, 65))}
	for _, id := range invalid {
		if budget.ValidID(id) {
			t.Errorf("ValidID(%q) = true, want false", id)
		}
	}
}

func TestPrincipalString(t *testing.T) {
	cases := []struct {
		p    budget.Principal
		want string
	}{
		{budget.Principal{Kind: budget.KindAgent, ID: "peer-7"}, "agent:peer-7"},
		{budget.Principal{Kind: budget.KindUser, ID: "eli"}, "user:eli"},
		{budget.Principal{Kind: budget.KindToken, ID: "alice"}, "token:alice"},
		{budget.Principal{Kind: budget.KindAddr, ID: "203.0.113.9"}, "ip:203.0.113.9"},
		{budget.Principal{}, "ip:unknown"},
	}
	for _, tc := range cases {
		if got := tc.p.String(); got != tc.want {
			t.Errorf("String() = %q, want %q", got, tc.want)
		}
	}
}

func TestRefusalMessageAndError(t *testing.T) {
	cases := []struct {
		scope   budget.Scope
		message string
	}{
		{budget.ScopeRate, "rate limit exceeded"},
		{budget.ScopeBusy, "too many concurrent answers"},
		{budget.ScopeIdentityDaily, "daily quota exhausted"},
		{budget.ScopeAddressDaily, "daily quota exhausted"},
		{budget.ScopeGlobalDaily, "daily quota exhausted"},
	}
	for _, tc := range cases {
		r := &budget.Refusal{Scope: tc.scope}
		if got := r.Message(); got != tc.message {
			t.Errorf("Message() for %q = %q, want %q", tc.scope, got, tc.message)
		}
		if r.Error() == "" {
			t.Errorf("Error() for %q is empty", tc.scope)
		}
	}
	if _, ok := budget.AsRefusal(errors.New("other")); ok {
		t.Error("AsRefusal accepted a non-refusal error")
	}
}

// acquireRefusal runs one Acquire whose refusal is the expected outcome,
// releasing the slot first when it was granted.
func acquireRefusal(t *testing.T, l *budget.Limiter, c budget.Caller) error {
	t.Helper()
	release, err := l.Acquire(c)
	if release != nil {
		release()
	}
	return err
}

// TestUnwritableCounterPathFailsAtStartup pins the fail-closed behaviour: a
// counter path the process cannot write must not start a budget that only lives
// in memory, because a restart would silently refund the day.
func TestUnwritableCounterPathFailsAtStartup(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if _, err := budget.New(budget.Config{
		Bounds: budget.Bounds{PerDay: 1},
		Path:   filepath.Join(blocked, "quota.json"),
	}); err == nil {
		t.Fatal("New() accepted an unwritable counter path")
	}
}

// TestConcurrentChargesAllPersist proves the lock-free write path is lossless:
// a later revision's snapshot contains every earlier increment, so dropping a
// stale snapshot cannot lose a count, and the final file holds them all.
func TestConcurrentChargesAllPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota.json")
	l, _ := newLimiter(t, budget.Bounds{}, path)

	const n = 64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			release, err := l.Acquire(budget.Caller{
				Principal: budget.Principal{Kind: budget.KindAgent, ID: fmt.Sprintf("peer-%d", i)},
				Address:   "203.0.113.9",
			})
			if err != nil {
				t.Errorf("charge %d refused: %v", i, err)
				return
			}
			release()
		}(i)
	}
	wg.Wait()

	if got := l.Snapshot().Global; got != n {
		t.Errorf("in-memory global = %d, want %d", got, n)
	}
	reopened, err := budget.New(budget.Config{Bounds: budget.Bounds{}, Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.Snapshot().Global; got != n {
		t.Errorf("persisted global = %d, want %d (a stale snapshot overwrote a newer one)", got, n)
	}
}
