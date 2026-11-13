// Package budget bounds what a public, token-less deployment may spend on the
// chat model. One Acquire call must pass a per-identity minute rate, a
// per-identity daily quota, a per-source-address daily quota, and a global
// daily quota, and it reserves one of a fixed number of in-flight slots. The
// daily counters persist in a 0600 JSON file beside the index, because
// `agent serve` opens the index read-only and must never write it.
//
// The identity is self-asserted (an A2A agentId, an X-User-Id, or the peer
// address), so it is an accounting key, never an authorization decision: a
// peer that rotates it is still bounded by the per-address and global daily
// gates. The global gate is the number that actually bounds a model bill; a
// per-minute rate alone allows 43,200 requests a day.
package budget

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
	"github.com/eli-l/elilap.dev-agent/internal/ratelimit"
)

// dayLayout is the UTC day key the counters roll over at.
const dayLayout = "2006-01-02"

// maxKeys caps the persisted counter maps. A peer that invents a new identity
// for every request must not grow the file without bound; past the cap an
// identity is simply not tracked, so only the per-address and global gates
// apply to it.
const maxKeys = 10_000

// Scope is the gate that refused a request, reported to the caller and logged.
type Scope string

const (
	// ScopeRate is the per-identity minute bucket.
	ScopeRate Scope = "rate"
	// ScopeIdentityDaily is the per-identity daily quota.
	ScopeIdentityDaily Scope = "identity_daily"
	// ScopeAddressDaily is the per-source-address daily quota.
	ScopeAddressDaily Scope = "address_daily"
	// ScopeGlobalDaily is the process-wide daily quota: the spend ceiling.
	ScopeGlobalDaily Scope = "global_daily"
	// ScopeBusy is the in-flight cap.
	ScopeBusy Scope = "busy"
)

// Refusal reports which gate refused a request and when to come back. Callers
// map it to a 429 (and a Retry-After) or to a JSON-RPC error.
type Refusal struct {
	Scope      Scope
	RetryAfter time.Duration
}

// Error implements error.
func (r *Refusal) Error() string { return "budget: " + string(r.Scope) + " exhausted" }

// Message is the caller-facing text for the refusal. The scope itself is
// reported separately, so a client can tell a minute burst from a spent day.
func (r *Refusal) Message() string {
	switch r.Scope {
	case ScopeRate:
		return "rate limit exceeded"
	case ScopeBusy:
		return "too many concurrent answers"
	default:
		return "daily quota exhausted"
	}
}

// AsRefusal extracts a *Refusal from an Acquire error.
func AsRefusal(err error) (*Refusal, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// Kind labels who a request is charged to.
type Kind string

const (
	// KindAgent is an A2A peer's self-asserted agentId.
	KindAgent Kind = "agent"
	// KindUser is an /v1/ask caller's self-asserted user id.
	KindUser Kind = "user"
	// KindToken is a configured bearer token id; it wins over a self-asserted
	// identity because it is the only verified one.
	KindToken Kind = "token"
	// KindAddr is the peer address, used when nothing else is asserted.
	KindAddr Kind = "ip"
)

// Principal is the identity an answer is charged to. ID is self-asserted for
// agent and user, so it must never be treated as an authorization decision.
type Principal struct {
	Kind Kind
	ID   string
}

// String renders the principal as the budget key and the audit label, e.g.
// "agent:peer-7", "user:eli", "token:alice", "ip:203.0.113.9".
func (p Principal) String() string {
	if p.Kind == "" || p.ID == "" {
		return "ip:unknown"
	}
	return string(p.Kind) + ":" + p.ID
}

// Caller is one request's billing identity plus the address it is also charged
// against. Address may be empty when it cannot be determined.
type Caller struct {
	Principal Principal
	Address   string
}

// Bounds is the configured public spend ceiling. A zero field disables that
// gate; `agent serve` refuses a public bind with a disabled daily gate.
type Bounds struct {
	// PerMinute is the per-identity minute rate (0 disables).
	PerMinute int
	// PerDay is the per-identity daily quota (0 disables).
	PerDay int
	// PerAddressPerDay is the per-source-address daily quota (0 disables).
	PerAddressPerDay int
	// GlobalPerDay is the process-wide daily quota: the spend ceiling.
	GlobalPerDay int
	// MaxInflight caps concurrent answers (0 disables).
	MaxInflight int
}

// Config configures a Limiter.
type Config struct {
	Bounds Bounds
	// Path is the counter file. Empty keeps counters in memory only.
	Path string
	// Now overrides the clock; production leaves it nil for the wall clock.
	Now func() time.Time
}

// Limiter enforces one Config's gates. The zero value is unusable; use New.
type Limiter struct {
	bounds Bounds
	path   string
	now    func() time.Time
	rate   *ratelimit.Limiter
	slots  chan struct{}

	mu         sync.Mutex
	day        string
	identities map[string]int
	addresses  map[string]int
	global     int
	revision   uint64 // incremented per charge, orders persisted snapshots

	saveMu     sync.Mutex
	written    uint64 // highest revision written to the counter file
	saveWarned bool
}

// state is the persisted counter file.
type state struct {
	Day        string         `json:"day"`
	Global     int            `json:"global"`
	Identities map[string]int `json:"identities,omitempty"`
	Addresses  map[string]int `json:"addresses,omitempty"`
}

// New builds a limiter and loads its persisted counters. A missing file starts
// an empty day. An unreadable or unparsable file is an error: a budget that
// silently resets itself on a corrupt counter file would fail open, so the
// operator has to look at it.
func New(cfg Config) (*Limiter, error) {
	l := &Limiter{
		bounds:     cfg.Bounds,
		path:       cfg.Path,
		now:        cfg.Now,
		identities: make(map[string]int),
		addresses:  make(map[string]int),
	}
	if l.now == nil {
		l.now = time.Now
	}
	l.rate = ratelimit.New(cfg.Bounds.PerMinute)
	l.rate.SetNow(l.now)
	if cfg.Bounds.MaxInflight > 0 {
		l.slots = make(chan struct{}, cfg.Bounds.MaxInflight)
	}
	if l.path != "" {
		if err := l.load(); err != nil {
			return nil, err
		}
		// Fail at startup, not on the first answer: a counter directory the
		// process cannot write would enforce the ceiling only in memory, and
		// every restart would hand out a fresh day.
		if err := l.probeWritable(); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// Bounds returns the configured gates.
func (l *Limiter) Bounds() Bounds {
	if l == nil {
		return Bounds{}
	}
	return l.bounds
}

// Snapshot is the current UTC day's usage, for startup and shutdown logs.
type Snapshot struct {
	Day        string
	Global     int
	Identities int
	Addresses  int
}

// Snapshot returns the current day's counters.
func (l *Limiter) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rolloverLocked(l.dayKey(l.now()))
	return Snapshot{
		Day:        l.day,
		Global:     l.global,
		Identities: len(l.identities),
		Addresses:  len(l.addresses),
	}
}

// Acquire charges one answer to the caller and returns the function that frees
// the in-flight slot. Every gate must pass; a refusal is a *Refusal and burns
// no daily quota. The minute bucket is charged first, so a daily or busy
// refusal may consume one rate credit, which refills on its own.
func (l *Limiter) Acquire(c Caller) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	idKey := c.Principal.String()
	if retry, ok := l.rate.Allow(idKey); !ok {
		return nil, &Refusal{Scope: ScopeRate, RetryAfter: retry}
	}

	l.mu.Lock()
	release, snapshot, revision, err := l.chargeLocked(c, idKey)
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	// Persist outside the gate: the counters are already incremented and the
	// in-flight slot is held, so a slow disk delays only this answer's reply,
	// never another peer's Acquire or Snapshot.
	l.save(snapshot, revision)
	return release, nil
}

// chargeLocked applies the daily gates and the in-flight cap, charges the
// winner, and returns the counter snapshot to persist. The caller holds l.mu.
func (l *Limiter) chargeLocked(c Caller, idKey string) (func(), state, uint64, error) {
	now := l.now()
	l.rolloverLocked(l.dayKey(now))

	addrKey := "ip:" + c.Address
	if addrKey == "ip:" {
		addrKey = "ip:unknown"
	}

	trackIdentity := l.bounds.PerDay > 0 && (l.identities[idKey] > 0 || len(l.identities) < maxKeys)
	trackAddress := l.bounds.PerAddressPerDay > 0 && (l.addresses[addrKey] > 0 || len(l.addresses) < maxKeys)

	if trackIdentity && l.identities[idKey] >= l.bounds.PerDay {
		return nil, state{}, 0, &Refusal{Scope: ScopeIdentityDaily, RetryAfter: untilMidnight(now)}
	}
	if trackAddress && l.addresses[addrKey] >= l.bounds.PerAddressPerDay {
		return nil, state{}, 0, &Refusal{Scope: ScopeAddressDaily, RetryAfter: untilMidnight(now)}
	}
	if l.bounds.GlobalPerDay > 0 && l.global >= l.bounds.GlobalPerDay {
		return nil, state{}, 0, &Refusal{Scope: ScopeGlobalDaily, RetryAfter: untilMidnight(now)}
	}

	release, err := l.takeSlot()
	if err != nil {
		return nil, state{}, 0, err
	}

	if trackIdentity {
		l.identities[idKey]++
	}
	if trackAddress {
		l.addresses[addrKey]++
	}
	l.global++

	// Copy the maps so the write can happen outside the lock. Any later
	// revision's snapshot includes this one's increment, which is why save may
	// drop an older revision without losing a count.
	l.revision++
	return release, state{
		Day:        l.day,
		Global:     l.global,
		Identities: maps.Clone(l.identities),
		Addresses:  maps.Clone(l.addresses),
	}, l.revision, nil
}

// takeSlot reserves an in-flight place without waiting: a queued answer would
// outlive the server's write timeout, so a full server refuses instead.
func (l *Limiter) takeSlot() (func(), error) {
	if l.slots == nil {
		return func() {}, nil
	}
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-l.slots }) }, nil
	default:
		return nil, &Refusal{Scope: ScopeBusy, RetryAfter: time.Second}
	}
}

// ValidID reports whether a self-asserted identity is acceptable. Ids become
// map keys, a log field, and the audit label, so the alphabet is narrow and the
// length is bounded.
func ValidID(id string) bool {
	return idPattern.MatchString(id)
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// PeerAddress extracts the peer address for the per-address gate. It never
// trusts a forwarded header: the card already refuses to derive its URL from
// one, and a spoofable address key would let a peer mint budgets at will.
func PeerAddress(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// dayKey renders the UTC day for a timestamp.
func (l *Limiter) dayKey(now time.Time) string { return now.UTC().Format(dayLayout) }

// rolloverLocked clears the counters when the UTC day changes.
func (l *Limiter) rolloverLocked(day string) {
	if day == l.day {
		return
	}
	l.day = day
	l.identities = make(map[string]int)
	l.addresses = make(map[string]int)
	l.global = 0
}

// untilMidnight is the daily gates' Retry-After.
func untilMidnight(now time.Time) time.Duration {
	utc := now.UTC()
	next := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return next.Sub(utc)
}

// probeWritable verifies the counter directory exists and accepts writes.
func (l *Limiter) probeWritable() error {
	dir := filepath.Dir(l.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("budget: create counter directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".quota-probe-*")
	if err != nil {
		return fmt.Errorf("budget: counter file %s is not writable: %w", l.path, err)
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("budget: counter file %s is not writable: %w", l.path, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("budget: counter file %s is not writable: %w", l.path, err)
	}
	return nil
}

// load reads the persisted counters. A file from an earlier day contributes
// nothing; it is rewritten on the next charge.
func (l *Limiter) load() error {
	b, err := os.ReadFile(l.path)
	if errors.Is(err, os.ErrNotExist) {
		l.day = l.dayKey(l.now())
		return nil
	}
	if err != nil {
		return fmt.Errorf("budget: read %s: %w", l.path, err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("budget: parse %s: %w", l.path, err)
	}
	l.day = st.Day
	if l.day == "" {
		l.day = l.dayKey(l.now())
	}
	if l.day == l.dayKey(l.now()) {
		l.global = st.Global
		if st.Identities != nil {
			l.identities = st.Identities
		}
		if st.Addresses != nil {
			l.addresses = st.Addresses
		}
	}
	return nil
}

// save persists a counter snapshot outside the gate lock. Writes are serialized
// by saveMu, and a snapshot older than the last written one is dropped: counter
// revisions only grow, and any later snapshot already contains the increment, so
// dropping is lossless. A write failure is logged once and the in-memory
// counters stay enforced, so a disk hiccup cannot turn the budget off.
func (l *Limiter) save(st state, revision uint64) {
	if l.path == "" {
		return
	}
	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	if revision <= l.written {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		l.warnSave(err)
		return
	}
	if err := writeAtomic(l.path, b); err != nil {
		l.warnSave(err)
		return
	}
	l.written = revision
	l.saveWarned = false
}

func (l *Limiter) warnSave(err error) {
	if l.saveWarned {
		return
	}
	l.saveWarned = true
	log := logging.Main()
	log.Warn().Err(err).Str("path", l.path).
		Msg("budget: persist counters failed; enforcing in memory")
}

// writeAtomic replaces path with data at mode 0600 via a same-directory
// temporary file, so a crash leaves the previous counters intact.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("budget: create counter directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".quota-*")
	if err != nil {
		return fmt.Errorf("budget: write counters: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("budget: write counters: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("budget: write counters: %w", err)
	}
	// Sync before the rename: without it a crash can commit the rename while
	// the data blocks are missing, and load treats an unparsable file as fatal.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("budget: sync counters: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("budget: write counters: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("budget: replace counters: %w", err)
	}
	return nil
}
