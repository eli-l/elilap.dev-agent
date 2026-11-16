// Package notify delivers the two webhooks this workflow needs: the owner is
// told that a peer submitted a question, and the peer is told that its
// question was answered.
//
// Peer-supplied URLs are the risky ones: an A2A client chooses them, so the
// sender is an SSRF boundary. A peer URL must be https, must resolve to a
// public address, and is dialed by the vetted address (never by the name
// again), which closes the DNS-rebinding hole a validate-then-request check
// leaves open. `--push-allow-insecure` relaxes that for loopback testing and
// is refused on a public bind: https is mandatory where the service is public.
//
// The owner notification URL is operator configuration, not peer input, so it
// may be a private http endpoint (a local hook, a home automation host).
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// HeaderToken is the A2A push-notification token header: the peer's shared
// secret, echoed back so the receiver can tell our notification from noise.
const HeaderToken = "X-A2A-Notification-Token"

// HeaderSignature carries an HMAC-SHA256 of the body keyed by the same secret,
// because the token header alone does not prove the body came from us.
const HeaderSignature = "X-Elilap-Signature"

// Policy is the sender's security posture.
type Policy struct {
	// AllowInsecure permits http and private/loopback destinations. It exists
	// for loopback testing only; `agent serve --bind-public` refuses it.
	AllowInsecure bool
	// AllowHosts, when non-empty, restricts destinations to these hosts:
	// an exact name or a ".suffix" match. Empty allows any public host.
	AllowHosts []string
	// MaxAttempts bounds retries; zero means 3.
	MaxAttempts int
	// AttemptTimeout bounds one HTTP attempt; zero means 5s.
	AttemptTimeout time.Duration
	// Backoff is the base delay between attempts; zero means 500ms.
	Backoff time.Duration
}

// ErrBlocked reports a destination the policy refuses.
var ErrBlocked = errors.New("notification destination refused")

// Sender delivers webhooks under one Policy.
type Sender struct {
	policy Policy
	client *http.Client
}

// New builds a sender. The transport dials only vetted addresses, so a
// hostname that resolves differently on the second lookup cannot redirect the
// request into a private network.
func New(p Policy) *Sender {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.AttemptTimeout <= 0 {
		p.AttemptTimeout = 5 * time.Second
	}
	if p.Backoff <= 0 {
		p.Backoff = 500 * time.Millisecond
	}
	dialer := &net.Dialer{Timeout: p.AttemptTimeout}
	transport := &http.Transport{
		Proxy:                 nil, // never let an environment proxy bypass the check
		DialContext:           guardedDial(p.AllowInsecure, dialer),
		TLSHandshakeTimeout:   p.AttemptTimeout,
		ResponseHeaderTimeout: p.AttemptTimeout,
		DisableKeepAlives:     true,
	}
	return &Sender{
		policy: p,
		client: &http.Client{
			Transport: transport,
			Timeout:   p.AttemptTimeout,
			// A redirect to another host would be an unvalidated second
			// destination, so a notification is delivered where it was
			// registered or not at all.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// ValidateURL checks a destination against the policy before anything is
// stored. The returned error never echoes the URL: a peer may have put
// credentials in its query string.
func (s *Sender) ValidateURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: url is required", ErrBlocked)
	}
	if len(raw) > 2048 {
		return nil, fmt.Errorf("%w: url is too long", ErrBlocked)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: url is not an absolute http(s) URL", ErrBlocked)
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%w: url must be absolute, without userinfo or a fragment", ErrBlocked)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !s.policy.AllowInsecure {
			return nil, fmt.Errorf("%w: https is required", ErrBlocked)
		}
	default:
		return nil, fmt.Errorf("%w: url must use https", ErrBlocked)
	}
	if len(s.policy.AllowHosts) > 0 && !hostAllowed(u.Hostname(), s.policy.AllowHosts) {
		return nil, fmt.Errorf("%w: host is not in the configured allowlist", ErrBlocked)
	}
	if !s.policy.AllowInsecure {
		// Reject a literal private address now; a name that resolves privately
		// is refused at dial time by guardedDial.
		if ip := net.ParseIP(u.Hostname()); ip != nil && !isPublic(ip) {
			return nil, fmt.Errorf("%w: destination must be a public address", ErrBlocked)
		}
	}
	return u, nil
}

// Send delivers one JSON payload, retrying network errors, 429, and 5xx. It
// reports how many attempts were made so the caller can record them.
func (s *Sender) Send(ctx context.Context, rawURL, token string, payload any) (int, error) {
	u, err := s.ValidateURL(rawURL)
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode notification: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= s.policy.MaxAttempts; attempt++ {
		if attempt > 1 {
			delay := s.policy.Backoff * time.Duration(1<<(attempt-2))
			if delay > 10*time.Second {
				delay = 10 * time.Second
			}
			select {
			case <-ctx.Done():
				return attempt - 1, ctx.Err()
			case <-time.After(delay):
			}
		}
		lastErr = s.post(ctx, u.String(), token, body)
		if lastErr == nil {
			return attempt, nil
		}
		var status *statusError
		if errors.As(lastErr, &status) && !status.retryable() {
			return attempt, lastErr
		}
	}
	return s.policy.MaxAttempts, lastErr
}

// statusError is a non-2xx response.
type statusError struct{ code int }

func (e *statusError) Error() string {
	return fmt.Sprintf("notification endpoint returned HTTP %d", e.code)
}

// retryable reports whether the status is worth another attempt.
func (e *statusError) retryable() bool {
	return e.code == http.StatusTooManyRequests || e.code >= 500
}

// post performs one attempt.
func (s *Sender) post(ctx context.Context, target, token string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build notification: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "elilap-dev-agent/"+agentVersion)
	if token != "" {
		req.Header.Set(HeaderToken, token)
		mac := hmac.New(sha256.New, []byte(token))
		mac.Write(body)
		req.Header.Set(HeaderSignature, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// *url.Error embeds the destination, query string included, and a peer
		// may have put a credential there. Report the cause, never the URL.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("notification request failed: %w", urlErr.Err)
		}
		return err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused/closed cleanly;
	// the body is never logged and never trusted.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &statusError{code: resp.StatusCode}
	}
	return nil
}

// agentVersion is a fixed UA fragment; the notification protocol, not the
// binary version, is what a receiver should branch on.
const agentVersion = "0.1"

// guardedDial dials only addresses the policy permits. Resolving here rather
// than in ValidateURL is what makes the check hold for the actual connection.
func guardedDial(allowPrivate bool, dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("notification dial: %w", err)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("notification dial: resolve host: %w", err)
		}
		var (
			lastErr error
			allowed bool
		)
		for _, ipa := range ips {
			if !allowPrivate && !isPublic(ipa.IP) {
				continue
			}
			allowed = true
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ipa.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if !allowed {
			return nil, fmt.Errorf("%w: host has no public address", ErrBlocked)
		}
		return nil, lastErr
	}
}

// isPublic rejects every address family that cannot be a peer's public webhook.
func isPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		// Judge the 4-byte form against every non-global block, not just
		// RFC1918: CGNAT (100.64/10, which holds Alibaba Cloud's metadata
		// address 100.100.100.200), benchmarking, documentation, reserved and
		// broadcast space are all reachable from inside a host but must never
		// be a peer's webhook target.
		return isGlobalIPv4(ip4)
	}
	// NAT64 and 6to4 addresses embed an IPv4 address; To4 returns nil for them,
	// so a naive check would treat 64:ff9b::a9fe:a9fe (NAT64 of 169.254.169.254)
	// as public and reopen the SSRF hole on a DNS64/6to4 host.
	if embedded := embeddedIPv4(ip); embedded != nil {
		return isPublic(embedded)
	}
	// Site-local, documentation, benchmarking, Teredo, ORCHID, and discard-only
	// ranges are not peer webhooks either; IsPrivate covers only ULA.
	if isNonGlobalIPv6(ip) {
		return false
	}
	return true
}

// nonGlobalIPv6 lists the IPv6 special-use prefixes that are not globally
// routable destinations and that net.IP's generic checks do not cover.
var nonGlobalIPv6 = []net.IPNet{
	parseCIDR("100::/64"),      // discard-only
	parseCIDR("2001::/32"),     // Teredo (embeds an IPv4 endpoint)
	parseCIDR("2001:2::/48"),   // benchmarking
	parseCIDR("2001:10::/28"),  // ORCHID (deprecated)
	parseCIDR("2001:20::/28"),  // ORCHIDv2
	parseCIDR("2001:db8::/32"), // documentation
	parseCIDR("3fff::/20"),     // documentation (RFC 9637)
	parseCIDR("fec0::/10"),     // site-local (deprecated, still routable)
}

// isNonGlobalIPv6 reports membership of the special-use IPv6 prefixes above.
func isNonGlobalIPv6(ip net.IP) bool {
	ip16 := ip.To16()
	if ip16 == nil || ip.To4() != nil {
		return false
	}
	for _, block := range nonGlobalIPv6 {
		if block.Contains(ip16) {
			return true
		}
	}
	return false
}

// nonGlobalIPv4 lists the blocks that are not globally routable destinations.
// Go's IsPrivate covers only RFC1918 + ULA, so a bare !IsPrivate treats CGNAT,
// benchmark, documentation, reserved, and broadcast space as public.
var nonGlobalIPv4 = []net.IPNet{
	parseCIDR("0.0.0.0/8"),       // "this network"
	parseCIDR("10.0.0.0/8"),      // RFC1918
	parseCIDR("100.64.0.0/10"),   // CGNAT, incl. cloud metadata endpoints
	parseCIDR("127.0.0.0/8"),     // loopback
	parseCIDR("169.254.0.0/16"),  // link-local, incl. 169.254.169.254
	parseCIDR("172.16.0.0/12"),   // RFC1918
	parseCIDR("192.0.0.0/24"),    // IETF protocol assignments
	parseCIDR("192.0.2.0/24"),    // TEST-NET-1
	parseCIDR("192.88.99.0/24"),  // 6to4 relay anycast (deprecated)
	parseCIDR("192.168.0.0/16"),  // RFC1918
	parseCIDR("198.18.0.0/15"),   // benchmarking
	parseCIDR("198.51.100.0/24"), // TEST-NET-2
	parseCIDR("203.0.113.0/24"),  // TEST-NET-3
	parseCIDR("224.0.0.0/4"),     // multicast
	parseCIDR("240.0.0.0/4"),     // reserved + limited broadcast
}

// parseCIDR parses a literal at init; a typo here is a programming error.
func parseCIDR(cidr string) net.IPNet {
	_, block, err := net.ParseCIDR(cidr)
	if err != nil {
		panic("notify: bad CIDR " + cidr + ": " + err.Error())
	}
	return *block
}

// isGlobalIPv4 reports whether an IPv4 address is a globally routable
// destination: outside every special-use block.
func isGlobalIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, block := range nonGlobalIPv4 {
		if block.Contains(ip4) {
			return false
		}
	}
	return true
}

// embeddedIPv4 decodes the IPv4 address carried by the well-known transition
// prefixes: NAT64 (64:ff9b::/96), 6to4 (2002::/16), and the deprecated
// IPv4-compatible form (::/96, excluding :: and ::1).
func embeddedIPv4(ip net.IP) net.IP {
	ip = ip.To16()
	if ip == nil {
		return nil
	}
	switch {
	case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
		ip[4] == 0 && ip[5] == 0 && ip[6] == 0 && ip[7] == 0 &&
		ip[8] == 0 && ip[9] == 0 && ip[10] == 0 && ip[11] == 0:
		return net.IPv4(ip[12], ip[13], ip[14], ip[15])
	case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b &&
		ip[4] == 0x00 && ip[5] == 0x01:
		// 64:ff9b:1::/48 is the local-use NAT64 prefix (RFC 8215). RFC 6052
		// places the IPv4 address in the bits right after the prefix, so for a
		// /48 it is bytes 6..9, not the trailing four bytes.
		return net.IPv4(ip[6], ip[7], ip[8], ip[9])
	case ip[0] == 0x20 && ip[1] == 0x02:
		return net.IPv4(ip[2], ip[3], ip[4], ip[5])
	case isZero(ip[:12]):
		// ::/96 is IPv4-compatible; :: and ::1 are handled above by the
		// unspecified/loopback checks.
		return net.IPv4(ip[12], ip[13], ip[14], ip[15])
	}
	return nil
}

// isZero reports whether every byte is zero.
func isZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// hostAllowed matches an exact host or a ".suffix" entry, so an operator can
// allow one receiver ("hooks.example.com") or a subtree (".example.com").
func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range allow {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, ".") {
			if strings.HasSuffix(host, entry) && len(host) > len(entry) {
				return true
			}
			continue
		}
		if host == entry {
			return true
		}
	}
	return false
}

// Dispatcher runs owner notifications off the request path: a submission must
// be acknowledged as soon as it is durable, not after a webhook round trip.
// The queue is bounded, and a full queue drops the notification with a warning
// rather than stalling an answer.
type Dispatcher struct {
	sender *Sender
	jobs   chan job
	wg     sync.WaitGroup

	// mu guards closed and deadline: Publish holds it for reading while it
	// sends, so Close cannot close the channel underneath a sender (which
	// would panic the process on a submission that outlives a shutdown).
	mu       sync.RWMutex
	closed   bool
	deadline time.Time
}

type job struct {
	url     string
	token   string
	payload any
}

// NewDispatcher starts one worker with a bounded queue.
func NewDispatcher(sender *Sender, queue int) *Dispatcher {
	if queue <= 0 {
		queue = 32
	}
	d := &Dispatcher{sender: sender, jobs: make(chan job, queue)}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for j := range d.jobs {
			// A shutdown sets a deadline so a dead receiver cannot hold the
			// process open for queue-length times the delivery timeout.
			d.mu.RLock()
			expired := !d.deadline.IsZero() && time.Now().After(d.deadline)
			d.mu.RUnlock()
			if expired {
				log := logging.HTTP()
				log.Warn().Msg("owner notification dropped at shutdown")
				continue
			}
			d.mu.RLock()
			deadline := d.deadline
			d.mu.RUnlock()
			budget := 30 * time.Second
			if !deadline.IsZero() {
				if remaining := time.Until(deadline); remaining < budget {
					budget = remaining
				}
			}
			if budget <= 0 {
				log := logging.HTTP()
				log.Warn().Msg("owner notification dropped at shutdown")
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			attempts, err := d.sender.Send(ctx, j.url, j.token, j.payload)
			cancel()
			if err != nil {
				log := logging.HTTP()
				log.Warn().Err(err).Int("attempts", attempts).Msg("owner notification failed")
			}
		}
	}()
	return d
}

// Publish queues a notification; it reports false when the queue is full or the
// dispatcher is closed. It is safe to call from a request handler that is still
// running while Close executes: a late publish is dropped, never sent on a
// closed channel.
func (d *Dispatcher) Publish(url, token string, payload any) bool {
	if d == nil || strings.TrimSpace(url) == "" {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return false
	}
	select {
	case d.jobs <- job{url: url, token: token, payload: payload}:
		return true
	default:
		log := logging.HTTP()
		log.Warn().Msg("owner notification queue full; dropping notification")
		return false
	}
}

// Close stops accepting notifications and lets the worker finish, at most
// drainTimeout past the close, so a dead owner webhook cannot delay shutdown
// indefinitely.
func (d *Dispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.deadline = time.Now().Add(drainTimeout)
	close(d.jobs)
	d.mu.Unlock()
	// Bound the wait as well as the per-job budget: a job already in flight
	// when Close runs captured its budget before the deadline existed, so
	// wg.Wait alone could still block for the full retry sequence. The worker
	// is abandoned (the process is exiting) rather than delaying shutdown.
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainTimeout + time.Second):
		log := logging.HTTP()
		log.Warn().Msg("owner notifications still in flight at shutdown; abandoning")
	}
}

// drainTimeout bounds the work a Close will still attempt.
const drainTimeout = 5 * time.Second
