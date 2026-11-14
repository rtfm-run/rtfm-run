// Package diagnose classifies why quickstart steps broke, using rules that
// work for any language or tool. It reads the agent's self-report and
// cross-checks it against the commands that actually ran in the devbox.
package diagnose

import (
	"fmt"
	"regexp"
	"strings"

	"vanguard.run/internal/report"
)

type rule struct {
	kind report.FailureKind
	re   *regexp.Regexp
	// explain builds the diagnosis; m is the regexp submatch.
	explain func(m []string) string
}

func fixed(s string) func([]string) string { return func([]string) string { return s } }

// Rules are tried in order; the first match wins.
var rules = []rule{
	{report.KindMissingPrereq, regexp.MustCompile(`(?m)(?:^|\s|:)([\w.+-]+): (?:command not found|not found)\s*$`),
		func(m []string) string {
			return fmt.Sprintf("`%s` isn't available on a fresh machine and the docs don't install it before using it.", m[1])
		}},
	{report.KindMissingPrereq, regexp.MustCompile(`'([\w.+-]+)' is not recognized as an internal or external command`),
		func(m []string) string {
			return fmt.Sprintf("`%s` isn't available on a fresh machine and the docs don't install it before using it.", m[1])
		}},
	{report.KindStaleCommand, regexp.MustCompile(`(?i)unrecognized arguments?:?\s*(\S*)|unknown (?:flag|option|shorthand flag|command)[:\s]+['"]?([\w-]*)|no such option:?\s*(\S*)|flag provided but not defined:?\s*(\S*)|unexpected argument '([^']*)'|invalid choice:?\s*'?([^'\s]*)|is not a [\w-]+ command`),
		func(m []string) string {
			for _, g := range m[1:] {
				if g != "" {
					return fmt.Sprintf("The documented command uses `%s`, which the tool no longer accepts.", g)
				}
			}
			return "The documented command or flag is not accepted by the current version of the tool."
		}},
	{report.KindStaleCommand, regexp.MustCompile(`(?i)the following arguments are required:?\s*(\S*)`),
		func(m []string) string {
			return fmt.Sprintf("The documented command is missing a required argument (%s).", strings.TrimRight(m[1], ","))
		}},
	{report.KindMissingPrereq, regexp.MustCompile(`(?i)no module named '?([\w.]+)|cannot find module '([^']+)'|cannot find package "?([^"\s]+)|could not find a version that satisfies|no matching distribution found|requires (?:a different )?python|unsupported engine|command failed: \S+ not installed`),
		func(m []string) string {
			for _, g := range m[1:] {
				if g != "" {
					return fmt.Sprintf("A dependency (`%s`) is missing; the docs don't install it.", g)
				}
			}
			return "A dependency or runtime version the docs assume isn't installed."
		}},
	{report.KindDeprecatedAPI, regexp.MustCompile(`(?i)deprecated|has been removed|was removed in|no longer (?:supported|available)|is not found for api version|model[^\n]{0,60}(?:not found|does not exist|is not supported)|end[- ]of[- ]life|has been retired|has been sunset`),
		fixed("The docs use an API, model or feature that has been deprecated or removed.")},
	{report.KindEnvironment, regexp.MustCompile(`(?i)api[_ -]?key|unauthenticated|unauthorized|permission_denied|\b40[13]\b|credentials? (?:not found|missing|required)|not logged in|login required|authentication required|must be authenticated|quota exceeded|environment variable [\w]+ (?:is )?(?:not set|missing|required)`),
		fixed("The step needs credentials or configuration the docs don't explain how to set up.")},
	{report.KindBrokenLink, regexp.MustCompile(`(?i)\b404\b|could not resolve host|repository not found|no such file or directory`),
		fixed("A file, URL or repository the docs reference doesn't exist.")},
}

// Classify returns the failure kind and a one-line diagnosis for output text
// from a failed command. Unmatched failures are KindUnknown.
func Classify(text string, exitCode int) (report.FailureKind, string) {
	for _, r := range rules {
		if m := r.re.FindStringSubmatch(text); m != nil {
			return r.kind, r.explain(m)
		}
	}
	if exitCode == 127 {
		return report.KindMissingPrereq, "A command the docs use isn't available on a fresh machine."
	}
	if exitCode != 0 {
		return report.KindUnknown, fmt.Sprintf("The step failed (exit %d) for a reason the rules don't recognize.", exitCode)
	}
	return report.KindUnknown, "The step failed for a reason the rules don't recognize."
}

// Apply classifies every failed or unsupported step in run, in place, and
// flags steps the agent reported as passing although the commands that
// actually ran say otherwise.
func Apply(run *report.Run) {
	for i := range run.Steps {
		s := &run.Steps[i]
		switch s.Status {
		case report.StatusFail:
			s.Failure, s.Diagnosis = classifyAttempts(run, s)

		case report.StatusUnsupported:
			if s.DocCommand != "" && s.Command != "" && normalize(s.DocCommand) != normalize(s.Command) {
				s.Failure = report.KindStaleCommand
				s.Diagnosis = fmt.Sprintf("Docs say `%s`; what actually worked was `%s`.", s.DocCommand, s.Command)
				if text, code := evidence(run, s, s.DocCommand); code != 0 && text != "" {
					if k, d := Classify(text, code); k != report.KindUnknown {
						s.Diagnosis += " " + d
					}
				}
				continue
			}
			s.Failure = report.KindDocGap
			s.Diagnosis = "The docs leave a gap here; the agent only got past it using outside knowledge."
			if s.Note != "" {
				s.Diagnosis += " " + s.Note
			}

		case report.StatusPass:
			if c := lastRun(run, s.Command); c != nil && c.ExitCode > 0 {
				s.Diagnosis = fmt.Sprintf("Reported as passing, but the last run of this command exited %d.", c.ExitCode)
			}
		}
	}
}

// classifyAttempts looks at every failed attempt of a step's command, in the
// order they ran. A newcomer hits the first problem first, so that is the
// primary failure; distinct later problems (e.g. "not installed", then after
// installing it, "needs an API key") are appended.
func classifyAttempts(run *report.Run, s *report.Step) (report.FailureKind, string) {
	var (
		primary report.FailureKind
		diag    []string
		seen    = map[report.FailureKind]bool{}
	)
	for _, c := range attempts(run, s.DocCommand, s.Command) {
		if c.ExitCode <= 0 {
			continue
		}
		k, d := Classify(c.Output, c.ExitCode)
		if seen[k] {
			continue
		}
		seen[k] = true
		if primary == report.KindNone {
			primary = k
			diag = append(diag, d)
		} else {
			diag = append(diag, "Then: "+d)
		}
	}
	if primary == report.KindNone || primary == report.KindUnknown {
		// Fall back to the agent's own excerpt and note.
		text, code := evidence(run, s, s.Command, s.DocCommand)
		if k, d := Classify(text, code); k != report.KindUnknown || primary == report.KindNone {
			return k, d
		}
	}
	return primary, strings.Join(diag, " ")
}

// attempts returns every observed command matching any of lines, in order.
func attempts(run *report.Run, lines ...string) []report.Command {
	want := map[string]bool{}
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			want[normalize(l)] = true
		}
	}
	var out []report.Command
	for _, c := range run.Commands {
		if want[normalize(c.Line)] {
			out = append(out, c)
		}
	}
	return out
}

// evidence gathers output text for a step: the agent's own excerpt and note,
// plus the output of the most recent matching command that actually ran.
// It returns the text and the best-known exit code.
func evidence(run *report.Run, s *report.Step, lines ...string) (string, int) {
	parts := []string{s.Output}
	code := s.ExitCode
	for _, l := range lines {
		if c := lastRun(run, l); c != nil {
			parts = append(parts, c.Output)
			if c.ExitCode > 0 {
				code = c.ExitCode
			}
			break
		}
	}
	parts = append(parts, s.Note)
	return strings.Join(parts, "\n"), code
}

// lastRun finds the last observed command matching line.
func lastRun(run *report.Run, line string) *report.Command {
	if strings.TrimSpace(line) == "" {
		return nil
	}
	want := normalize(line)
	for i := len(run.Commands) - 1; i >= 0; i-- {
		if normalize(run.Commands[i].Line) == want {
			return &run.Commands[i]
		}
	}
	return nil
}

func normalize(s string) string { return report.NormalizeCommand(s) }
