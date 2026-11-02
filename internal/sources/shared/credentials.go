package shared

import (
	"fmt"
	"strings"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
)

// ResolveEnvRef reads the environment variable named by a source's
// `{ env: VAR }` credential reference. An unset or blank variable is an error
// that names the variable; the value never appears in any message. Blank
// counts as missing because a .env copied from example.env contains the
// credential names with empty values, and config.MissingEnv reports the same
// set - validation and runtime must agree.
//
// Credential names are configuration data (sources.yaml decides them), so
// they cannot be envconfig struct fields; envcfg.Lookup is the single
// accessor for them. Providers call this instead of touching the environment.
func ResolveEnvRef(ref config.EnvRef) (string, error) {
	if ref.Env == "" {
		return "", fmt.Errorf("credential %q: no env var configured", ref.Env)
	}
	v, ok := envcfg.Lookup(ref.Env)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("credential environment variable %s is not set", ref.Env)
	}
	// Return the value verbatim: a credential is opaque, so trimming it here
	// would silently alter a secret that legitimately has edge whitespace.
	return v, nil
}
