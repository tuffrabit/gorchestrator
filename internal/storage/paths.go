package storage

import (
	"fmt"
	"path"
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
