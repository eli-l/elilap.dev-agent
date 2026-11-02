package ingest_test

import (
	"testing"
	"time"

	"github.com/eli-l/elilap.dev-agent/internal/ingest"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{in: "2026-10-26T00:00:00Z", want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		{in: "2026-10-26", want: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		{in: "2026-10-26T09:30:00", want: time.Date(2026, 10, 26, 9, 30, 0, 0, time.UTC)},
		{in: "168h", want: now.Add(-168 * time.Hour)},
		{in: "7d", want: now.Add(-7 * 24 * time.Hour)},
		{in: "1w", want: now.Add(-7 * 24 * time.Hour)},
		{in: "1.5d", want: now.Add(-36 * time.Hour)},
		{in: "", wantErr: true},
		{in: "yesterday", wantErr: true},
		{in: "-7d", wantErr: true},
	}
	for _, tc := range tests {
		got, err := ingest.ParseSince(tc.in, now)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseSince(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSince(%q) error = %v", tc.in, err)
			continue
		}
		if !got.Equal(tc.want) {
			t.Errorf("ParseSince(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
