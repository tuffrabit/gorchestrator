package storage

import "testing"

func TestValidateRelativePath(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"research/result.json", false},
		{"../etc/passwd", true},
		{"/etc/passwd", true},
		{"", true},
		{"a/../../b", true},
		{"plan/attempts/1/output.md", false},
	}
	for _, tc := range cases {
		err := ValidateRelativePath(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("%q: want error", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%q: %v", tc.in, err)
		}
	}
}

func TestJoinContained(t *testing.T) {
	key, err := JoinContained("projects/1/issues/2", "research/result.json")
	if err != nil {
		t.Fatal(err)
	}
	if key != "projects/1/issues/2/research/result.json" {
		t.Fatalf("key = %q", key)
	}
	if _, err := JoinContained("projects/1/issues/2", "../3/secret"); err == nil {
		t.Fatal("expected escape rejection")
	}
}
