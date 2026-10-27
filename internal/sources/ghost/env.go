package ghost

import (
	"fmt"
	"os"

	"github.com/eli-l/elilap.dev-agent/internal/config"
)

// resolveEnvRef reads the env var named by ref. An unset variable is an
// error that names the variable; the value never appears in any message.
func resolveEnvRef(ref config.EnvRef) (string, error) {
	if ref.Env == "" {
		return "", fmt.Errorf("credential %q: no env var configured", ref.Env)
	}
	v, ok := os.LookupEnv(ref.Env)
	if !ok || v == "" {
		return "", fmt.Errorf("credential environment variable %s is not set", ref.Env)
	}
	return v, nil
}
