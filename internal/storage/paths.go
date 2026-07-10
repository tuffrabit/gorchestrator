package storage

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// IssueDir returns the relative path for an issue directory.
func IssueDir(projectID, issueID int64) string {
	return path.Join("projects", fmt.Sprintf("%d", projectID), "issues", fmt.Sprintf("%d", issueID))
}

// PhaseDir returns the relative path for a phase directory under an issue.
func PhaseDir(projectID, issueID int64, phase string) string {
	return path.Join(IssueDir(projectID, issueID), phase)
}

// TaskPath returns the relative path to task.json for a phase.
func TaskPath(projectID, issueID int64, phase string) string {
	return path.Join(PhaseDir(projectID, issueID, phase), "task.json")
}

// ResultPath returns the relative path to result.json for a phase.
func ResultPath(projectID, issueID int64, phase string) string {
	return path.Join(PhaseDir(projectID, issueID, phase), "result.json")
}

// EventsPath returns the relative path to events.jsonl for a phase.
func EventsPath(projectID, issueID int64, phase string) string {
	return path.Join(PhaseDir(projectID, issueID, phase), "events.jsonl")
}

// AttemptDir returns the relative path to an attempt directory for a phase.
func AttemptDir(projectID, issueID int64, phase string, attempt int) string {
	return path.Join(PhaseDir(projectID, issueID, phase), "attempts", fmt.Sprintf("%d", attempt))
}

// AttemptOutputPath returns the relative path to an attempt's output file.
func AttemptOutputPath(projectID, issueID int64, phase string, attempt int) string {
	return path.Join(AttemptDir(projectID, issueID, phase, attempt), "output.md")
}

// FeedbackPath returns the relative path to adjudicator feedback for an attempt.
func FeedbackPath(projectID, issueID int64, phase string, attempt int) string {
	return path.Join(AttemptDir(projectID, issueID, phase, attempt), "feedback.md")
}

// OutputPath returns the relative path to output.md for the first attempt.
// It is kept for callers that have not been migrated to the attempts layout.
func OutputPath(projectID, issueID int64, phase string) string {
	return AttemptOutputPath(projectID, issueID, phase, 1)
}

// SourcePath returns the relative path to the read-only source snapshot for an issue.
func SourcePath(projectID, issueID int64) string {
	return path.Join(IssueDir(projectID, issueID), "source")
}

// WorkspacePath returns the relative path to the implementer's workspace for an issue.
func WorkspacePath(projectID, issueID int64) string {
	return path.Join(PhaseDir(projectID, issueID, "implementation"), "workspace")
}

// RepoCachePath returns the relative path to the project's bare git clone cache.
func RepoCachePath(projectID int64) string {
	return path.Join("repos", fmt.Sprintf("%d.git", projectID))
}

// Abs joins storage root with a slash-canonical relative key for host paths
// (git worktrees, container mounts). Prefer this over ad-hoc filepath joins.
func Abs(storageRoot, relKey string) string {
	return filepath.Join(storageRoot, filepath.FromSlash(relKey))
}

// ValidateRelativePath rejects absolute paths and parent-directory segments.
func ValidateRelativePath(rel string) error {
	if rel == "" {
		return fmt.Errorf("path is empty")
	}
	if path.IsAbs(rel) || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) {
		return fmt.Errorf("absolute paths are not allowed")
	}
	// Reject ".." before Clean, which would resolve them away and hide escapes.
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return fmt.Errorf("path traversal is not allowed")
		}
	}
	clean := path.Clean(rel)
	if clean == "." || clean == "" {
		return fmt.Errorf("path is empty")
	}
	if strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("path traversal is not allowed")
	}
	return nil
}

// JoinContained joins base and rel after validating rel, using forward-slash keys.
func JoinContained(base, rel string) (string, error) {
	if err := ValidateRelativePath(rel); err != nil {
		return "", err
	}
	clean := path.Clean(path.Join(base, rel))
	// Ensure clean stays under base (separator-aware).
	if clean != base && !strings.HasPrefix(clean, base+"/") {
		return "", fmt.Errorf("path escapes issue directory")
	}
	return clean, nil
}
