package shared

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/logging"
)

// Provider HTTP defaults. A full Ghost ingest issues one request per post, so
// a single transient 429/503 must not abort the run; a stalled connection must
// not hang it either.
const (
	// DefaultHTTPTimeout bounds one request (including its body read).
	DefaultHTTPTimeout = 60 * time.Second
	// DefaultRetryAttempts is the total number of attempts per request.
	DefaultRetryAttempts = 3
	// DefaultRetryBaseDelay is the first backoff step; it doubles per attempt.
	DefaultRetryBaseDelay = 500 * time.Millisecond
	// maxRetryDelay caps both the exponential backoff and a Retry-After hint,
	// so a misbehaving server cannot park an ingest for minutes.
	maxRetryDelay = 30 * time.Second
	// drainLimit is how much of a discarded response body is read before the
	// connection is returned to the pool.
	drainLimit = 64 << 10
)

// NewHTTPClient returns the HTTP client providers use: a per-request timeout
// plus a transport that retries transient upstream failures. Every provider
// shares its shapes, so retry behavior lives here instead of in each client.
//
// A non-positive timeout means DefaultHTTPTimeout.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = DefaultHTTPTimeout
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &RetryTransport{
			Base:      http.DefaultTransport,
			Attempts:  DefaultRetryAttempts,
			BaseDelay: DefaultRetryBaseDelay,
		},
	}
}

// RetryTransport retries requests that failed in a way that may succeed on a
// second try: network errors, 408, 429, and 5xx. It only retries requests it
// can replay (GET/HEAD, or a body with GetBody), because resending a
// non-replayable body would send the wrong payload.
//
// It is an http.RoundTripper, so neither provider client needs to know about
// retries: they build one shared client and the transport handles the rest.
type RetryTransport struct {
	// Base is the underlying transport; nil means http.DefaultTransport.
	Base http.RoundTripper
	// Attempts is the total number of attempts per request (1 = no retry).
	Attempts int
	// BaseDelay is the first backoff; it doubles after each failed attempt.
	BaseDelay time.Duration
}

// RoundTrip implements http.RoundTripper.
func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	attempts := t.Attempts
	if attempts <= 0 {
		attempts = 1
	}
	delay := t.BaseDelay
	if delay <= 0 {
		delay = DefaultRetryBaseDelay
	}

	log := logging.Sources()
	for attempt := 1; ; attempt++ {
		resp, err := base.RoundTrip(req)
		if attempt >= attempts || !retryable(req, resp, err) {
			return resp, RedactError(err)
		}
		wait := delay
		status := 0
		if resp != nil {
			status = resp.StatusCode
			if ra, ok := retryAfter(resp.Header); ok {
				wait = ra
			}
			drainAndClose(resp)
		}
		if wait > maxRetryDelay {
			wait = maxRetryDelay
		}
		log.Warn().
			Str("method", req.Method).
			Str("path", req.URL.Path).
			Int("status", status).
			Err(RedactError(err)).
			Int("attempt", attempt).
			Int("attempts", attempts).
			Dur("wait", wait).
			Msg("retrying transient upstream failure")

		if err := sleepCtx(req.Context(), wait); err != nil {
			return nil, err
		}
		// RoundTrippers must not be handed a mutated request that a previous
		// attempt already consumed, so each retry gets a replayable clone.
		next := req.Clone(req.Context())
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			next.Body = body
		}
		req = next
		delay *= 2
	}
}

// RedactError returns err with any URL query string removed, so an error can
// be logged or returned without leaking credentials that travel as query
// parameters (the Ghost Content API key, for example).
func RedactError(err error) error {
	if err == nil {
		return nil
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	redacted := *ue
	if u, perr := url.Parse(ue.URL); perr == nil {
		u.RawQuery = ""
		u.Fragment = ""
		redacted.URL = u.String()
	}
	return &redacted
}

// retryable reports whether a failed round trip may succeed when replayed.
func retryable(req *http.Request, resp *http.Response, err error) bool {
	if !replayable(req) {
		return false
	}
	if err != nil {
		// A canceled or timed-out context is the caller's decision; retrying
		// would only burn the remaining budget.
		return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	switch resp.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// replayable reports whether the request can be sent again with the same
// payload.
func replayable(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return req.GetBody != nil
}

// retryAfter parses a Retry-After hint (delta-seconds or HTTP date).
func retryAfter(h http.Header) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		d := time.Until(at)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

// drainAndClose reads a bounded amount of a response body so the underlying
// connection can be reused, then closes it.
func drainAndClose(resp *http.Response) {
	if resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
	_ = resp.Body.Close()
}

// sleepCtx waits for d, returning early when the context ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
