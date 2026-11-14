// Package fix turns a newhire run's evidence into doc fixes.
//
// Fixes come from evidence, never from hardcoded, tool-specific rules:
//   - FromRun: when the agent got past a step by running something other
//     than what the doc said, and that command really succeeded in the
//     devbox, the doc's command is replaced with the one that worked.
//   - The fixer agent (see persona.RenderFixer) investigates the remaining
//     problems in the repo and on the web and edits the doc directly.
package fix

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"vanguard.run/internal/report"
)

// Applied is a substitution made from run evidence.
type Applied struct {
	StepIndex int
	Original  string // the doc's command
	Proposed  string // the command that actually worked
}

// FromRun applies evidence-backed command substitutions to doc and returns
// the new text plus what was applied. A substitution is made only when:
//   - the step failed or was unsupported,
//   - the doc's command and the command that worked differ,
//   - the working command's last real run exited 0, and
//   - the doc's command appears as a whole command on its own line (a
//     shell prompt like "$ " and indentation are preserved). Substrings of
//     longer commands are never touched.
func FromRun(run *report.Run, doc string) (string, []Applied) {
	var applied []Applied
	for _, s := range run.Steps {
		if s.Status != report.StatusFail && s.Status != report.StatusUnsupported {
			continue
		}
		orig, prop := strings.TrimSpace(s.DocCommand), strings.TrimSpace(s.Command)
		if orig == "" || prop == "" || squash(orig) == squash(prop) {
			continue
		}
		if c := run.LastCommand(prop); c == nil || c.ExitCode != 0 {
			continue
		}
		if looksMachineSpecific(prop) {
			continue
		}
		next, changed := replaceCommand(doc, orig, prop)
		if !changed {
			continue
		}
		doc = next
		applied = append(applied, Applied{StepIndex: s.Index, Original: orig, Proposed: report.StripPrompt(prop)})
	}
	return doc, applied
}

// looksMachineSpecific rejects commands that embed paths of the run's own
// throwaway workspace, which would be wrong in published docs.
func looksMachineSpecific(cmd string) bool {
	return strings.Contains(cmd, "/vanguard/ws-") || strings.Contains(cmd, "/tmp/vanguard")
}

func squash(s string) string { return report.NormalizeCommand(s) }

// replaceCommand replaces lines whose whole command equals orig, keeping
// each line's indentation and prompt marker.
func replaceCommand(doc, orig, prop string) (string, bool) {
	want := squash(orig)
	newCmd := strings.TrimSpace(report.StripPrompt(prop))
	lines := strings.Split(doc, "\n")
	changed := false
	for i, l := range lines {
		indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		body := l[len(indent):]
		prompt := ""
		for _, p := range []string{"$ ", "% ", "> "} {
			if strings.HasPrefix(body, p) {
				prompt, body = p, body[len(p):]
				break
			}
		}
		if squash(body) == want {
			lines[i] = indent + prompt + newCmd
			changed = true
		}
	}
	return strings.Join(lines, "\n"), changed
}

// Problems renders a run's issues as plain text for the fixer agent.
func Problems(run *report.Run) string {
	var b strings.Builder
	for _, s := range run.Steps {
		if s.Status != report.StatusFail && s.Status != report.StatusUnsupported {
			continue
		}
		fmt.Fprintf(&b, "Step %d [%s, %s]: %s\n", s.Index, s.Status, s.Failure, s.Instruction)
		if s.DocRef != "" {
			fmt.Fprintf(&b, "  Location: %s\n", s.DocRef)
		}
		if s.DocCommand != "" {
			fmt.Fprintf(&b, "  Doc says: %s\n", s.DocCommand)
		}
		if s.Command != "" && squash(s.Command) != squash(s.DocCommand) {
			fmt.Fprintf(&b, "  Newcomer ran instead: %s\n", s.Command)
		}
		if s.Diagnosis != "" {
			fmt.Fprintf(&b, "  Diagnosis: %s\n", s.Diagnosis)
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "  Newcomer's note: %s\n", s.Note)
		}
		if out := failingOutput(run, s); out != "" {
			fmt.Fprintf(&b, "  Output:\n%s\n", indent(out, "    "))
		}
		b.WriteString("\n")
	}
	if run.GaveUpReason != "" {
		fmt.Fprintf(&b, "The newcomer gave up: %s\n", run.GaveUpReason)
	}
	return strings.TrimSpace(b.String())
}

// failingOutput returns output from the first failed real attempt of the
// step's command, falling back to the agent's own excerpt.
func failingOutput(run *report.Run, s report.Step) string {
	for _, line := range []string{s.DocCommand, s.Command} {
		want := squash(line)
		if want == "" {
			continue
		}
		for _, c := range run.Commands {
			if c.ExitCode > 0 && squash(c.Line) == want && strings.TrimSpace(c.Output) != "" {
				return lastLines(c.Output, 15)
			}
		}
	}
	return lastLines(s.Output, 15)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func indent(s, prefix string) string {
	return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// UnifiedDiff returns a unified diff from before to after, labeled a/<name>
// and b/<name>. It returns "" when they are identical.
func UnifiedDiff(ctx context.Context, name, before, after string) (string, error) {
	if before == after {
		return "", nil
	}
	dir, err := os.MkdirTemp("", "vanguard-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(a, []byte(before), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(b, []byte(after), 0o644); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "diff", "-u", "--label", "a/"+name, "--label", "b/"+name, a, b)
	var out bytes.Buffer
	cmd.Stdout = &out
	err = cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 { // 1 = files differ
		return out.String(), nil
	}
	if err != nil {
		return "", fmt.Errorf("diff: %w", err)
	}
	return out.String(), nil
}

// FirstError returns the most telling error line from the step's first
// failed real attempt (the last non-empty line of its output), for summaries.
func FirstError(run *report.Run, s report.Step) string {
	out := failingOutput(run, s)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "…(truncated)") {
			continue
		}
		if r := []rune(l); len(r) > 160 {
			l = string(r[:157]) + "..."
		}
		return l
	}
	return ""
}
