package store

import "testing"

func TestFTSQuery(t *testing.T) {
	tests := []struct {
		text   string
		want   string
		wantOK bool
	}{
		{text: "Harness vendor lock-in", want: `"harness" OR "vendor" OR "lock" OR "in"`, wantOK: true},
		{text: "harness harness HARNESS", want: `"harness"`, wantOK: true},
		{text: "C++23 and don't", want: `"23" OR "and" OR "don't"`, wantOK: true},
		{text: `"quoted" -minus`, want: `"quoted" OR "minus"`, wantOK: true},
		{text: "a I 1 !!!", want: "", wantOK: false},
		{text: "   ", want: "", wantOK: false},
	}
	for _, tc := range tests {
		got, ok := FTSQuery(tc.text)
		if ok != tc.wantOK {
			t.Errorf("FTSQuery(%q) ok = %v, want %v", tc.text, ok, tc.wantOK)
			continue
		}
		if got != tc.want {
			t.Errorf("FTSQuery(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}
