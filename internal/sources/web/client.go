package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// Fetch limits.
const (
	// maxBodyBytes caps one response. Pages are HTML documents; anything this
	// large is either a mislabeled binary or a page no chunker should see.
	maxBodyBytes = 8 << 20
	// maxSnippet is how much of an error body an error message carries.
	maxSnippet = 256
	// defaultUserAgent is used when the config declares no defaults.user_agent.
	defaultUserAgent = "elilap-dev-agent/0.1 (+https://elilap.dev)"
)

// Client is the provider's HTTP layer: one polite, size-capped GET with a
// stable User-Agent and an optional robots.txt gate. It deliberately knows
// nothing about HTML; parsing lives in extract.go.
type Client struct {
	hc     *http.Client
	ua     string
	delay  time.Duration
	robots bool
	// host pins every request (including redirects) to the configured site.
	// Empty means unpinned, which only tests should use.
	host string
	now  func() time.Time

	mu   sync.Mutex
	last time.Time // last request start, for the politeness delay

	// robotsByHost caches only definitive robots.txt answers (a parsed file or
	// a 404), so a transient failure is retried instead of disabling the gate
	// for the rest of the run.
	robotsMu     sync.Mutex
	robotsByHost map[string]*robotsRules
}

// newClient builds the HTTP layer for one provider. A nil http.Client gets
// http.DefaultClient; a blank user agent gets the built-in one; an empty host
// leaves redirects unpinned.
func newClient(hc *http.Client, ua string, delay time.Duration, checkRobots bool, host string) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = defaultUserAgent
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host != "" {
		hc = pinRedirects(hc, host)
	}
	return &Client{
		hc:     hc,
		ua:     ua,
		delay:  delay,
		robots: checkRobots,
		host:   host,
		now:    time.Now,
	}
}

// response is one completed GET.
type response struct {
	// URL is the final URL after redirects.
	URL *url.URL
	// Status is the HTTP status code.
	Status int
	// Header carries the response headers.
	Header http.Header
	// Body is the response body, capped at maxBodyBytes.
	Body []byte
}

// get performs one GET. A non-2xx answer is a statusError. The robots gate
// runs first when enabled; a robots.txt that cannot be read is treated as
// permissive (the log names the failure) so a missing file never blocks a
// site, while a readable one is always obeyed.
func (c *Client) get(ctx context.Context, target *url.URL) (*response, error) {
	return c.do(ctx, target, c.robots)
}

// do is get with the robots gate switchable, so loading robots.txt cannot
// recurse into the gate that is being loaded.
func (c *Client) do(ctx context.Context, target *url.URL, checkRobots bool) (*response, error) {
	// CheckRedirect pins every hop, but the first URL must be pinned here:
	// a caller passing an off-site target must not reach the network at all.
	if !c.sameSite(target) {
		return nil, fmt.Errorf("web: refusing to fetch off-site URL %s", target.Redacted())
	}
	if checkRobots {
		ok, err := c.allowed(ctx, target)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("web: %s: %w", target.Redacted(), errRobotsDisallowed)
		}
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("web: build request for %s: %w", target.Redacted(), err)
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web: get %s: %w", target.Redacted(), err)
	}
	defer resp.Body.Close()

	// A redirect is the monitored site's decision, not the operator's: pin the
	// final host so a 3xx cannot pull an internal or unrelated host into the
	// index (or reach a metadata endpoint).
	if !c.sameSite(resp.Request.URL) {
		return nil, fmt.Errorf("web: %s redirected off-site to %s", target.Redacted(), resp.Request.URL.Redacted())
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("web: read %s: %w", target.Redacted(), err)
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("web: %s exceeds the %d-byte response cap", target.Redacted(), maxBodyBytes)
	}

	final := resp.Request.URL
	out := &response{URL: final, Status: resp.StatusCode, Header: resp.Header, Body: body}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &statusError{
			URL:     target.Redacted(),
			Status:  resp.StatusCode,
			Snippet: truncateSnippet(string(body)),
		}
	}
	return out, nil
}

// statusError is a non-2xx answer, with a trimmed body for the log.
type statusError struct {
	URL     string
	Status  int
	Snippet string
}

func (e *statusError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("web: %s: unexpected status %d", e.URL, e.Status)
	}
	return fmt.Sprintf("web: %s: unexpected status %d: %s", e.URL, e.Status, e.Snippet)
}

// errRobotsDisallowed reports a robots.txt refusal. Optional probes (the
// conventional feed paths) treat it as "not served here", exactly like a 404.
var errRobotsDisallowed = errors.New("disallowed by robots.txt")

// isNotExist reports whether err is a 404/410 answer, which discovery treats
// as "this optional document does not exist" rather than a failure.
func isNotExist(err error) bool {
	se, ok := err.(*statusError)
	if !ok {
		return false
	}
	return se.Status == http.StatusNotFound || se.Status == http.StatusGone
}

// isProbeAbsent reports whether an optional probe (a conventional feed path)
// answered with a status that means "not served here": missing, or refused for
// this client. Anything else is a failure the caller may need to surface.
func isProbeAbsent(err error) bool {
	if isNotExist(err) || errors.Is(err, errRobotsDisallowed) {
		return true
	}
	se, ok := err.(*statusError)
	if !ok {
		return false
	}
	return se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden
}

// wait enforces the politeness delay between two requests to the site. The
// delay is measured from the start of the previous request, so a slow response
// already provides the spacing.
func (c *Client) wait(ctx context.Context) error {
	if c.delay <= 0 {
		return nil
	}
	c.mu.Lock()
	start := c.now()
	var wait time.Duration
	if !c.last.IsZero() {
		if elapsed := start.Sub(c.last); elapsed < c.delay {
			wait = c.delay - elapsed
		}
	}
	c.last = start.Add(wait)
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// allowed consults the site's robots.txt. The rules are cached per origin, but
// only a definitive answer is cached: an unreachable file is treated as
// permissive for that request and retried on the next one, so one transient
// failure cannot switch the gate off for the whole run.
func (c *Client) allowed(ctx context.Context, target *url.URL) (bool, error) {
	site := &url.URL{Scheme: target.Scheme, Host: target.Host}
	c.robotsMu.Lock()
	cached, ok := c.robotsByHost[site.Host]
	c.robotsMu.Unlock()
	if !ok {
		rules, definitive := c.loadRobots(ctx, site)
		if definitive {
			c.robotsMu.Lock()
			if c.robotsByHost == nil {
				c.robotsByHost = make(map[string]*robotsRules, 1)
			}
			c.robotsByHost[site.Host] = rules
			c.robotsMu.Unlock()
		}
		cached = rules
	}
	return cached.allowed(target.RequestURI(), c.ua), nil
}

// loadRobots fetches and parses robots.txt and reports whether the result is
// definitive. An absent file is definitive (allow everything); a transient
// failure is not, so the next request tries again.
func (c *Client) loadRobots(ctx context.Context, site *url.URL) (*robotsRules, bool) {
	log := logging.Sources().With().Str("provider", "web").Logger()
	target := &url.URL{Scheme: site.Scheme, Host: site.Host, Path: "/robots.txt"}
	resp, err := c.do(ctx, target, false)
	if err != nil {
		if isNotExist(err) {
			log.Debug().Str("host", site.Host).Msg("no robots.txt, all paths allowed")
			return nil, true
		}
		if se, ok := err.(*statusError); ok && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
			// RFC 9309: a robots.txt that exists but denies access means the
			// whole site is off limits, not that the file is missing.
			log.Warn().Str("host", site.Host).Int("status", se.Status).
				Msg("robots.txt denied access, treating the site as disallowed")
			return &robotsRules{disallowAll: true}, true
		}
		log.Warn().Err(err).Str("host", site.Host).
			Msg("robots.txt could not be read, treating this request as permissive and retrying next time")
		return nil, false
	}
	rules := parseRobots(string(resp.Body))
	log.Debug().
		Str("host", site.Host).
		Int("groups", rules.groupCount()).
		Msg("robots.txt loaded")
	return rules, true
}

// sameSite reports whether u belongs to the configured site.
func (c *Client) sameSite(u *url.URL) bool { return sameSiteHost(c.host, u) }

// sameSiteHost compares a URL's host name with want. The www alias is
// accepted; ports and schemes are ignored, because a site may legitimately
// redirect http -> https, to port 443, or to its canonical name.
func sameSiteHost(want string, u *url.URL) bool {
	if want == "" || u == nil {
		return true
	}
	got := strings.ToLower(u.Hostname())
	want = strings.ToLower(want)
	return got == want || strings.TrimPrefix(got, "www.") == strings.TrimPrefix(want, "www.")
}

// pinRedirects returns a client whose CheckRedirect rejects a hop outside host
// *before* the request is sent, so a 3xx from the monitored site cannot reach
// an internal host even for one round trip. An existing policy is preserved
// and runs after the host check.
func pinRedirects(base *http.Client, host string) *http.Client {
	pinned := *base
	previous := base.CheckRedirect
	pinned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !sameSiteHost(host, req.URL) {
			return fmt.Errorf("web: redirected off-site to %s", req.URL.Redacted())
		}
		if previous != nil {
			return previous(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &pinned
}

// robotsRules is a parsed robots.txt. A nil *robotsRules allows everything;
// disallowAll carries a 401/403 answer, which RFC 9309 treats as a complete
// prohibition rather than a missing file.
type robotsRules struct {
	groups      []robotsGroup
	disallowAll bool
}

// robotsGroup is one user-agent block of robots.txt.
type robotsGroup struct {
	agents []string
	rules  []robotsRule
}

// robotsRule is one Allow/Disallow line.
type robotsRule struct {
	allow bool
	path  string
	re    *regexp.Regexp
}

// parseRobots parses the subset of robots.txt that matters for a page
// crawler: user-agent groups, Allow, and Disallow. Sitemap and Crawl-delay
// lines are ignored (discovery owns the sitemap, and the delay is configured).
func parseRobots(body string) *robotsRules {
	rules := &robotsRules{}
	var group robotsGroup
	flush := func() {
		if len(group.agents) > 0 {
			rules.groups = append(rules.groups, group)
		}
		group = robotsGroup{}
	}
	for _, line := range strings.Split(body, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			// RFC 9309: a blank line terminates a group. Without this an empty
			// "User-agent: ourbot" block would absorb the next block's rules.
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if len(group.rules) > 0 {
				flush()
			}
			if value != "" {
				group.agents = append(group.agents, strings.ToLower(value))
			}
		case "allow", "disallow":
			if len(group.agents) == 0 {
				continue
			}
			if value == "" {
				continue // an empty Disallow allows everything
			}
			group.rules = append(group.rules, newRobotsRule(key == "allow", value))
		}
	}
	flush()
	return rules
}

// newRobotsRule compiles one robots path pattern: "*" matches any run of
// characters, a trailing "$" anchors the end, everything else is literal.
func newRobotsRule(allow bool, pattern string) robotsRule {
	anchor := strings.HasSuffix(pattern, "$")
	pattern = strings.TrimSuffix(pattern, "$")
	var b strings.Builder
	b.WriteString("^")
	for _, part := range strings.Split(pattern, "*") {
		b.WriteString(regexp.QuoteMeta(part))
		b.WriteString(".*")
	}
	expr := strings.TrimSuffix(b.String(), ".*")
	if anchor {
		expr += "$"
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		// QuoteMeta output is always valid; this is unreachable but keeps the
		// zero rule harmless if a future pattern source changes.
		re = regexp.MustCompile(`^$`)
	}
	return robotsRule{allow: allow, path: pattern, re: re}
}

// groupCount reports how many user-agent groups were parsed (debug logging).
func (r *robotsRules) groupCount() int {
	if r == nil {
		return 0
	}
	return len(r.groups)
}

// allowed reports whether path may be fetched by the agent identified by ua.
// The matching groups are the ones naming this agent, else the "*" group. The
// longest matching rule wins; Allow wins a tie, per RFC 9309.
func (r *robotsRules) allowed(path string, ua string) bool {
	if r == nil {
		return true
	}
	if r.disallowAll {
		return false
	}
	if len(r.groups) == 0 {
		return true
	}
	token := strings.ToLower(strings.TrimSpace(ua))
	if i := strings.IndexAny(token, "/ "); i >= 0 {
		token = token[:i]
	}
	groups := r.groupsFor(token)
	var best *robotsRule
	for _, g := range groups {
		for i := range g.rules {
			rule := &g.rules[i]
			if !rule.re.MatchString(path) {
				continue
			}
			switch {
			case best == nil, len(rule.path) > len(best.path):
				best = rule
			case len(rule.path) == len(best.path) && rule.allow:
				best = rule
			}
		}
	}
	return best == nil || best.allow
}

// groupsFor returns the rule groups that apply to token: the groups naming
// it, or the wildcard groups when none does.
func (r *robotsRules) groupsFor(token string) []robotsGroup {
	var named []robotsGroup
	for _, g := range r.groups {
		for _, a := range g.agents {
			if a != "*" && a == token {
				named = append(named, g)
				break
			}
		}
	}
	if len(named) > 0 {
		return named
	}
	var wildcard []robotsGroup
	for _, g := range r.groups {
		for _, a := range g.agents {
			if a == "*" {
				wildcard = append(wildcard, g)
				break
			}
		}
	}
	return wildcard
}

// truncateSnippet shortens a response body for an error message. It counts
// runes so a multi-byte character is never split into invalid UTF-8.
func truncateSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= maxSnippet {
		return s
	}
	return string(r[:maxSnippet]) + "..."
}
