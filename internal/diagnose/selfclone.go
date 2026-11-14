package diagnose

import (
	"path"
	"regexp"
	"strings"

	"rtfm.run/internal/report"
)

// The newcomer works in a checkout of the repository under test, so a
// documented `git clone` of that same repository can fail only because the
// directory already exists. That's an artifact of the test setup, not a
// problem with the docs.

var splitRe = regexp.MustCompile(`&&|;|\n`)

// cloneURL returns the repository URL of a `git clone` command, or "".
func cloneURL(cmd string) string {
	f := strings.Fields(cmd)
	if len(f) < 3 || f[0] != "git" || f[1] != "clone" {
		return ""
	}
	for _, a := range f[2:] {
		if !strings.HasPrefix(a, "-") && strings.ContainsAny(a, "/:") {
			return a
		}
	}
	return ""
}

const alreadyExists = "already exists and is not an empty directory"

// repoName is the last path element of a repository URL, without ".git".
func repoName(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(u), "/"), ".git")
	if i := strings.LastIndexAny(u, ":/"); i >= 0 {
		u = u[i+1:]
	}
	return strings.ToLower(path.Base(u))
}

// onlyClonesSelf reports whether cmd does nothing but clone the repository
// under test (and maybe cd into it).
func onlyClonesSelf(cmd, repo string) bool {
	want := repoName(repo)
	cloned := false
	for _, part := range splitRe.Split(cmd, -1) {
		part = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(part), "$"))
		switch {
		case part == "":
		case strings.HasPrefix(part, "cd "):
		default:
			u := cloneURL(part)
			if u == "" || want == "" || repoName(u) != want {
				return false
			}
			cloned = true
		}
	}
	return cloned
}

// selfClone marks a failed step that only cloned the repository under test,
// and failed because the checkout already exists, as passed.
func selfClone(run *report.Run, s *report.Step) bool {
	if s.Status != report.StatusFail && s.Status != report.StatusUnsupported {
		return false
	}
	cmd := s.DocCommand
	if cmd == "" {
		cmd = s.Command
	}
	if !onlyClonesSelf(cmd, run.Repo) {
		return false
	}
	text := s.Output + "\n" + s.Note
	if c := lastRun(run, s.Command); c != nil {
		text += "\n" + c.Output
	}
	if !strings.Contains(text, alreadyExists) && s.Status == report.StatusFail {
		return false
	}
	s.Status = report.StatusPass
	s.Failure, s.Diagnosis = report.KindNone, ""
	s.Note = strings.TrimSpace("Already checked out: rtfm tests this repository from its own checkout, so cloning it again isn't needed. " + s.Note)
	return true
}
