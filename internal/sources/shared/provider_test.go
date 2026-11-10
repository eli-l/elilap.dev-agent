package shared_test

import (
	"net/http"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// TestBuildContextClientDefaults guards the documented contract: a constructor
// that receives a zero BuildContext must still get a usable HTTP client.
func TestBuildContextClientDefaults(t *testing.T) {
	if got := (shared.BuildContext{}).Client(); got != http.DefaultClient {
		t.Errorf("BuildContext{}.Client() = %p, want http.DefaultClient", got)
	}
	custom := &http.Client{}
	if got := (shared.BuildContext{HTTPClient: custom}).Client(); got != custom {
		t.Errorf("BuildContext{HTTPClient: x}.Client() = %p, want x", got)
	}
}
