package notify_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/notify"
)

// TestValidateURLIsStrict pins the default posture: https only, public only,
// no userinfo, and an optional host allowlist. These are the rules a peer's
// webhook must pass before the service will store it.
func TestValidateURLIsStrict(t *testing.T) {
	s := notify.New(notify.Policy{})
	for _, raw := range []string{
		"http://hooks.example.com/a",        // https is mandatory
		"https://127.0.0.1/a",               // loopback
		"https://10.0.0.5/a",                // private
		"https://[::1]/a",                   // IPv6 loopback
		"https://user:pass@hooks.example/a", // userinfo
		"https://hooks.example/a#frag",      // fragment
		"ftp://hooks.example/a",             // wrong scheme
		"/relative",                         // not absolute
		"",                                  // required
	} {
		if _, err := s.ValidateURL(raw); !errors.Is(err, notify.ErrBlocked) {
			t.Errorf("ValidateURL(%q) err = %v, want ErrBlocked", raw, err)
		}
	}
	if _, err := s.ValidateURL("https://hooks.example.com/a?x=1"); err != nil {
		t.Errorf("ValidateURL(public https) = %v, want nil", err)
	}
}

// TestIsPublicRejectsEmbeddedPrivateIPv4 pins the transition-prefix decode:
// a NAT64 or 6to4 literal that embeds a link-local/private IPv4 must not be
// treated as a public destination, or DNS64/6to4 hosts reopen the SSRF hole.
func TestIsPublicRejectsEmbeddedPrivateIPv4(t *testing.T) {
	s := notify.New(notify.Policy{})
	for _, raw := range []string{
		"https://[64:ff9b::a9fe:a9fe]/hook", // NAT64 of 169.254.169.254
		"https://[64:ff9b::a00:1]/hook",     // NAT64 of 10.0.0.1
		"https://[2002:a9fe:a9fe::1]/hook",  // 6to4 of 169.254.169.254
		"https://[2002:a00:1::1]/hook",      // 6to4 of 10.0.0.1
		"https://[::10.0.0.1]/hook",         // IPv4-compatible 10.0.0.1
	} {
		if _, err := s.ValidateURL(raw); !errors.Is(err, notify.ErrBlocked) {
			t.Errorf("ValidateURL(%q) err = %v, want ErrBlocked", raw, err)
		}
	}
	// IPv6 special-use ranges are not webhooks either; local-use NAT64 embeds
	// its IPv4 after the /48 prefix (RFC 6052), not in the trailing four bytes.
	for _, raw := range []string{
		"https://[64:ff9b:1:a9fe:a9fe::]/hook", // local-use NAT64 of 169.254.169.254
		"https://[fec0::1]/hook",               // site-local
		"https://[2001:db8::1]/hook",           // documentation
		"https://[2001:2::1]/hook",             // benchmarking
		"https://[2001::1]/hook",               // Teredo
		"https://[100::1]/hook",                // discard-only
		"https://[3fff::1]/hook",               // documentation (RFC 9637)
	} {
		if _, err := s.ValidateURL(raw); !errors.Is(err, notify.ErrBlocked) {
			t.Errorf("ValidateURL(%q) err = %v, want ErrBlocked", raw, err)
		}
	}

	// Genuinely public literals still pass — including a local-use NAT64 of a
	// public IPv4, so the decode is not a blanket rejection.
	for _, raw := range []string{
		"https://[64:ff9b:1:0808:0808::]/hook", // local-use NAT64 of 8.8.8.8
		"https://[2001:4860:4860::8888]/hook",
		"https://8.8.8.8/hook",
		"https://203.0.114.1/hook", // just outside TEST-NET-3
	} {
		if _, err := s.ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
}

// TestSendNeverEchoesTheDestination pins the privacy contract: a connection
// failure is reported by cause, because a peer may have put a credential in
// its webhook query string.
func TestSendNeverEchoesTheDestination(t *testing.T) {
	s := notify.New(notify.Policy{AllowInsecure: true, MaxAttempts: 1, AttemptTimeout: 200 * time.Millisecond})
	const secret = "s3cr3t-in-the-query"
	_, err := s.Send(context.Background(), "http://127.0.0.1:1/hook?token="+secret, "", map[string]string{"a": "b"})
	if err == nil {
		t.Fatal("Send to a closed port succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error leaks the destination query: %v", err)
	}
	if strings.Contains(err.Error(), "token=") {
		t.Errorf("error leaks the destination query string: %v", err)
	}
}

func TestValidateURLAllowlist(t *testing.T) {
	s := notify.New(notify.Policy{AllowHosts: []string{"hooks.example.com", ".corp.example"}})
	for _, raw := range []string{"https://hooks.example.com/a", "https://bot.corp.example/a"} {
		if _, err := s.ValidateURL(raw); err != nil {
			t.Errorf("ValidateURL(%q) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{"https://evil.example/a", "https://corp.example/a"} {
		if _, err := s.ValidateURL(raw); !errors.Is(err, notify.ErrBlocked) {
			t.Errorf("ValidateURL(%q) err = %v, want ErrBlocked", raw, err)
		}
	}
}

// TestAllowInsecureIsTheTestingEscapeHatch pins the loopback-only relaxation:
// a local http receiver is reachable when the operator opted in.
func TestAllowInsecureIsTheTestingEscapeHatch(t *testing.T) {
	s := notify.New(notify.Policy{AllowInsecure: true})
	if _, err := s.ValidateURL("http://127.0.0.1:9999/hook"); err != nil {
		t.Errorf("ValidateURL(loopback http) = %v, want nil", err)
	}
	if _, err := s.ValidateURL("http://10.0.0.5/hook"); err != nil {
		t.Errorf("ValidateURL(private http) = %v, want nil with AllowInsecure", err)
	}
}

func TestSendSignsAndDelivers(t *testing.T) {
	var (
		gotToken string
		gotSig   string
		gotBody  []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get(notify.HeaderToken)
		gotSig = r.Header.Get(notify.HeaderSignature)
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	s := notify.New(notify.Policy{AllowInsecure: true, MaxAttempts: 2, AttemptTimeout: time.Second})
	attempts, err := s.Send(context.Background(), srv.URL, "shared-secret", map[string]string{"hello": "world"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if gotToken != "shared-secret" {
		t.Errorf("token header = %q", gotToken)
	}
	if !strings.HasPrefix(gotSig, "sha256=") || len(gotSig) != len("sha256=")+64 {
		t.Errorf("signature header = %q, want sha256=<64 hex>", gotSig)
	}
	var payload map[string]string
	if err := json.Unmarshal(gotBody, &payload); err != nil || payload["hello"] != "world" {
		t.Errorf("payload = %s (%v)", gotBody, err)
	}
}

func TestSendRetriesTransientFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := notify.New(notify.Policy{AllowInsecure: true, MaxAttempts: 3, AttemptTimeout: time.Second, Backoff: time.Millisecond})
	attempts, err := s.Send(context.Background(), srv.URL, "", map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// TestSendDoesNotRetryAClientError keeps a misconfigured receiver from being
// hammered: only 429 and 5xx are worth another attempt.
func TestSendDoesNotRetryAClientError(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	s := notify.New(notify.Policy{AllowInsecure: true, MaxAttempts: 3, AttemptTimeout: time.Second, Backoff: time.Millisecond})
	attempts, err := s.Send(context.Background(), srv.URL, "", map[string]string{"a": "b"})
	if err == nil {
		t.Fatal("Send succeeded against a 400")
	}
	if attempts != 1 || calls.Load() != 1 {
		t.Errorf("attempts = %d, calls = %d; want 1", attempts, calls.Load())
	}
}

func TestSendRefusesBlockedURL(t *testing.T) {
	s := notify.New(notify.Policy{})
	if _, err := s.Send(context.Background(), "http://127.0.0.1/hook", "", map[string]string{}); !errors.Is(err, notify.ErrBlocked) {
		t.Errorf("err = %v, want ErrBlocked", err)
	}
}

// TestDispatcherDeliversAndCloses exercises the owner-notification path: a
// queued job is delivered and Close waits for the queue to drain.
func TestDispatcherDeliversAndCloses(t *testing.T) {
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case done <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := notify.NewDispatcher(notify.New(notify.Policy{AllowInsecure: true}), 4)
	if !d.Publish(srv.URL, "", map[string]string{"event": "knowledge_request.created"}) {
		t.Fatal("Publish returned false on an empty queue")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("notification was not delivered")
	}
	// A publish that races a shutdown must be dropped, never sent on a closed
	// channel (which would panic the process).
	d.Close()
	if d.Publish(srv.URL, "", map[string]string{"event": "late"}) {
		t.Error("Publish after Close reported success")
	}
	d.Close() // Close is idempotent.
}
