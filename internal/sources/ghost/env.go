package ghost

import (
	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

// resolveEnvRef reads the env var named by ref through the shared credential
// resolver. It stays as a thin provider-local alias so callers keep reading
// naturally; the rule (blank counts as missing, values never printed) lives
// in shared.ResolveEnvRef.
func resolveEnvRef(ref config.EnvRef) (string, error) {
	return shared.ResolveEnvRef(ref)
}
