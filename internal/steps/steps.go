// Package steps maps a run report's steps to stored, comparable steps: a
// stable key so the same doc step can be tracked across runs and commits,
// and whether it "checks out" in reality.
package steps

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"vanguard.run/internal/db"
	"vanguard.run/internal/report"
)

// Key identifies a doc step across runs: the doc plus its normalized
// command (or, for steps without one, its normalized instruction). Line
// numbers aren't used because they shift as the doc changes. occurrence
// disambiguates the same command appearing more than once.
func Key(docPath string, s report.Step, occurrence int) string {
	basis := "cmd:" + report.NormalizeCommand(s.DocCommand)
	if s.DocCommand == "" {
		basis = "text:" + strings.ToLower(strings.Join(strings.Fields(s.Instruction), " "))
	}
	h := sha256.Sum256([]byte(docPath + "\x00" + basis + fmt.Sprintf("\x00%d", occurrence)))
	return hex.EncodeToString(h[:12])
}

// ChecksOut reports whether a step works as documented, confirmed by
// reality: it passed, the agent ran the doc's own command (not a variant it
// worked out), and the devbox log shows that command really succeeded.
func ChecksOut(s report.Step, observed *report.Command) bool {
	if s.Status != report.StatusPass {
		return false
	}
	if s.DocCommand != "" && s.Command != "" && report.NormalizeCommand(s.DocCommand) != report.NormalizeCommand(s.Command) {
		return false
	}
	return observed == nil || observed.ExitCode == 0
}

// FromRun converts a run's steps for storage under stage.
func FromRun(run *report.Run, stage string) []db.Step {
	if run == nil {
		return nil
	}
	seen := map[string]int{}
	out := make([]db.Step, 0, len(run.Steps))
	for _, s := range run.Steps {
		basis := report.NormalizeCommand(s.DocCommand) + "|" + strings.ToLower(s.Instruction)
		seen[basis]++
		observed := run.LastCommand(s.Command)
		if observed == nil {
			observed = run.LastCommand(s.DocCommand)
		}
		var exit *int
		if observed != nil && observed.ExitCode >= 0 {
			e := observed.ExitCode
			exit = &e
		}
		out = append(out, db.Step{
			Stage:        stage,
			Idx:          s.Index,
			StepKey:      Key(run.DocPath, s, seen[basis]),
			DocRef:       s.DocRef,
			Instruction:  s.Instruction,
			DocCommand:   s.DocCommand,
			Command:      s.Command,
			ExitCode:     s.ExitCode,
			Status:       string(s.Status),
			Failure:      string(s.Failure),
			Diagnosis:    s.Diagnosis,
			Note:         s.Note,
			Output:       s.Output,
			ObservedExit: exit,
			ChecksOut:    ChecksOut(s, observed),
		})
	}
	return out
}

// Verdict summarizes a run for listings.
func Verdict(run *report.Run) string {
	switch {
	case run.Error != "":
		return "error"
	case run.Works():
		return "works"
	case run.Blocked():
		return "blocked"
	default:
		return "broken"
	}
}
