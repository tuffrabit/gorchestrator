package tools

import (
	"strings"
	"testing"
)

func TestMatchFileDirOnlyPattern(t *testing.T) {
	m, err := ParseGitignore(strings.NewReader("node_modules/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.MatchFile("node_modules/big.js") {
		t.Fatal("dirOnly pattern should ignore files under the directory")
	}
	if m.MatchFile("src/node_modules.txt") {
		t.Fatal("dirOnly pattern should not ignore similarly named files")
	}
}

func TestMatchFileNestedNegation(t *testing.T) {
	m, err := ParseGitignore(strings.NewReader("*.log\n!keep.log\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.MatchFile("logs/error.log") {
		t.Fatal("*.log should ignore nested log files")
	}
	if m.MatchFile("keep.log") {
		t.Fatal("negation should win")
	}
}

func TestParseGitignoreNilMatcherMatchesNothing(t *testing.T) {
	var m *GitignoreMatcher
	if m.MatchFile("anything.go") || m.Match("anything.go", false) {
		t.Fatal("nil matcher must match nothing")
	}
}
