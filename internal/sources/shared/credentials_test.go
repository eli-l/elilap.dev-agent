package shared_test

import (
	"strings"
	"testing"

	"github.com/eli-l/elilap.dev-agent/internal/config"
	"github.com/eli-l/elilap.dev-agent/internal/sources/shared"
)

func TestResolveEnvRef(t *testing.T) {
	t.Setenv("AGENT_TEST_CRED_SET", "  secret-with-edges  ")
	t.Setenv("AGENT_TEST_CRED_BLANK", "   ")

	tests := []struct {
		name    string
		env     string
		want    string
		wantErr string
	}{
		{
			// A .env copied from example.env has blank credentials, and
			// config.MissingEnv calls that missing; the resolver must agree.
			name: "set value is returned verbatim", env: "AGENT_TEST_CRED_SET", want: "  secret-with-edges  ",
		},
		{name: "blank counts as missing", env: "AGENT_TEST_CRED_BLANK", wantErr: "AGENT_TEST_CRED_BLANK"},
		{name: "unset variable", env: "AGENT_TEST_CRED_ABSENT", wantErr: "AGENT_TEST_CRED_ABSENT"},
		{name: "no reference configured", env: "", wantErr: "no env var configured"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := shared.ResolveEnvRef(config.EnvRef{Env: tc.env})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveEnvRef() = %q, nil error; want one containing %q", got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("ResolveEnvRef() error = %q, want it to contain %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secret-with-edges") {
					t.Errorf("ResolveEnvRef() error leaks a credential value: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveEnvRef() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveEnvRef() = %q, want %q (value must not be trimmed)", got, tc.want)
			}
		})
	}
}
