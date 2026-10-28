package ghost

import (
	"fmt"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/envcfg"
)

// resolveEnvRef reads the env var named by ref. An unset variable is an
// error that names the variable; the value never appears in any message.
//
// Credential names are dynamic (sources.yaml decides them), so they cannot be
// envconfig struct fields; envcfg.Lookup is the single accessor for them.
func resolveEnvRef(ref config.EnvRef) (string, error) {
	if ref.Env == "" {
		return "", fmt.Errorf("credential %q: no env var configured", ref.Env)
	}
	v, ok := envcfg.Lookup(ref.Env)
	if !ok || v == "" {
		return "", fmt.Errorf("credential environment variable %s is not set", ref.Env)
	}
	return v, nil
}
