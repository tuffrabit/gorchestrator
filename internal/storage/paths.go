package storage

import (
	"fmt"
	"path/filepath"
)

// IssueDir returns the relative path for an issue directory.
func IssueDir(projectID, issueID int64) string {
	return filepath.Join("projects", fmt.Sprintf("%d", projectID), "issues", fmt.Sprintf("%d", issueID))
}

// PhaseDir returns the relative path for a phase directory under an issue.
func PhaseDir(projectID, issueID int64, phase string) string {
	return filepath.Join(IssueDir(projectID, issueID), phase)
}

// TaskPath returns the relative path to task.json for a phase.
func TaskPath(projectID, issueID int64, phase string) string {
	return filepath.Join(PhaseDir(projectID, issueID, phase), "task.json")
}

// ResultPath returns the relative path to result.json for a phase.
func ResultPath(projectID, issueID int64, phase string) string {
	return filepath.Join(PhaseDir(projectID, issueID, phase), "result.json")
}

// OutputPath returns the relative path to output.md for a phase.
func OutputPath(projectID, issueID int64, phase string) string {
	return filepath.Join(PhaseDir(projectID, issueID, phase), "output.md")
}
