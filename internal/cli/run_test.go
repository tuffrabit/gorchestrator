package cli

import "testing"

// TestParseFlowFlag covers the -flow flag: ordered ids, and an error on an
// empty id (a typo like "a,,b" must not silently shorten the flow).
func TestParseFlowFlag(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
		err  bool
	}{
		{raw: "", want: nil},
		{raw: "  coder  ", want: []string{"coder"}},
		{raw: "scout,fixer", want: []string{"scout", "fixer"}},
		{raw: " scout , fixer ", want: []string{"scout", "fixer"}},
		{raw: "scout,,fixer", err: true},
		{raw: "scout,", err: true},
	}
	for _, tc := range cases {
		got, err := parseFlowFlag(tc.raw)
		if tc.err {
			if err == nil {
				t.Fatalf("parseFlowFlag(%q): expected error, got %#v", tc.raw, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseFlowFlag(%q): %v", tc.raw, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("parseFlowFlag(%q) = %#v, want %#v", tc.raw, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseFlowFlag(%q) = %#v, want %#v", tc.raw, got, tc.want)
			}
		}
	}
}
