package orchestrator

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Scope evaluation defaults (cheap heuristics; advisory, not a second content-sniff pipeline).
const (
	// maxScopeTitleDescRunes flags title+description combined length.
	maxScopeTitleDescRunes = 8000
	// maxAttachmentPeekBytes is the light-read cap per text attachment.
	maxAttachmentPeekBytes = 8 * 1024
	// maxAttachmentsToPeek limits how many attachments are scanned.
	maxAttachmentsToPeek = 8
)

// defaultScopePhrases are case-insensitive substrings that flag broad/runaway issues.
var defaultScopePhrases = []string{
	"refactor the entire",
	"refactor entire",
	"rewrite the whole",
	"rewrite entire",
	"migrate everything",
	"migrate all",
	"rebuild the entire",
	"rebuild entire",
	"overhaul the entire",
	"overhaul entire",
	"replace the entire",
	"replace everything",
	"rewrite everything",
	"redesign the entire",
	"redesign entire",
}

// ScopeHit is a positive scope-detection result.
type ScopeHit struct {
	// Reasons are human-readable signals (also joined into result.json error).
	Reasons []string
}

// Flagged reports whether any reason was recorded.
func (h ScopeHit) Flagged() bool {
	return len(h.Reasons) > 0
}

// Summary is a single-line reason for result.json / decision feedback.
func (h ScopeHit) Summary() string {
	if !h.Flagged() {
		return ""
	}
	return "scope: " + strings.Join(h.Reasons, "; ")
}

// ScopeAttachment is a small text attachment candidate for light reads.
type ScopeAttachment struct {
	Name string
	Data []byte // may be full file or already size-capped by caller
}

// EvaluateScope runs cheap heuristics over issue text and optional attachment peeks.
// It never panics; empty inputs are fine.
func EvaluateScope(title, description string, basenames []string, peeks []ScopeAttachment) ScopeHit {
	var hit ScopeHit

	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	combined := title
	if description != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += description
	}

	if n := utf8.RuneCountInString(combined); n > maxScopeTitleDescRunes {
		hit.Reasons = append(hit.Reasons,
			fmt.Sprintf("title+description length %d runes exceeds %d", n, maxScopeTitleDescRunes))
	}

	lowerCombined := strings.ToLower(combined)
	for _, phrase := range defaultScopePhrases {
		if phrase != "" && strings.Contains(lowerCombined, phrase) {
			hit.Reasons = append(hit.Reasons, fmt.Sprintf("forbidden phrase %q", phrase))
		}
	}

	// Basenames can carry signals ("migrate-everything.md").
	for _, name := range basenames {
		ln := strings.ToLower(strings.TrimSpace(name))
		if ln == "" {
			continue
		}
		for _, phrase := range defaultScopePhrases {
			// Phrase words without spaces for filename-ish matches.
			compact := strings.ReplaceAll(phrase, " ", "")
			if strings.Contains(ln, compact) || strings.Contains(ln, strings.ReplaceAll(phrase, " ", "-")) ||
				strings.Contains(ln, strings.ReplaceAll(phrase, " ", "_")) {
				hit.Reasons = append(hit.Reasons, fmt.Sprintf("attachment name %q matches broad scope phrase", name))
				break
			}
		}
	}

	// Light peeks into small text attachments (already extension-gated at upload).
	for i, p := range peeks {
		if i >= maxAttachmentsToPeek {
			break
		}
		data := p.Data
		if len(data) > maxAttachmentPeekBytes {
			data = data[:maxAttachmentPeekBytes]
		}
		// Skip if mostly non-text.
		if !looksMostlyText(data) {
			continue
		}
		body := strings.ToLower(string(data))
		for _, phrase := range defaultScopePhrases {
			if phrase != "" && strings.Contains(body, phrase) {
				hit.Reasons = append(hit.Reasons,
					fmt.Sprintf("attachment %q contains forbidden phrase %q", p.Name, phrase))
				break
			}
		}
	}

	// Dedupe reasons while preserving order.
	if len(hit.Reasons) > 1 {
		hit.Reasons = dedupeStrings(hit.Reasons)
	}
	return hit
}

// IsScopeHoldError reports whether a phase result error is a scope gate reason.
func IsScopeHoldError(errMsg string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(errMsg)), "scope:")
}

func looksMostlyText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	nonPrint := 0
	for _, c := range b {
		if c == 0 {
			return false
		}
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			nonPrint++
		}
	}
	return nonPrint*10 <= len(b) // ≤10% control bytes
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
