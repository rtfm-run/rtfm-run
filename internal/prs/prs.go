// Package prs turns a verified doc fix into a pull request: the branch,
// commit, title and a body that explains to maintainers how the problem was
// found, what changed and how the change was verified.
package prs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"rtfm.run/internal/fix"
	"rtfm.run/internal/github"
	"rtfm.run/internal/report"
)

// Proposal is everything needed to open the PR.
type Proposal struct {
	Repo          string // clone URL as tested
	BaseCommit    string // commit the run tested
	DocPath       string
	Original      []byte // doc as tested
	Fixed         []byte
	Patch         string
	Branch        string
	Title         string
	CommitMessage string
	Body          string
	// BaseBranch is the branch to target ("" = the repository default), e.g.
	// when CONTRIBUTING says to send changes to "develop".
	BaseBranch string
	// Author, if set, authors the commit (needed for DCO sign-off).
	Author *github.Identity
}

// ErrNotEligible means the run has no fix that should become a PR.
var ErrNotEligible = errors.New("not eligible for a pull request")

// Eligible reports whether run's fix may be proposed upstream: verified, or
// blocked only on credentials with every doc issue fixed. allowPartial also
// admits improved fixes.
func Eligible(run *report.Run, allowPartial bool) error {
	f := run.Fix
	if f == nil {
		return fmt.Errorf("%w: the run has no fix (it had no issues, or ran with --fix=false)", ErrNotEligible)
	}
	switch {
	case f.Patch == "":
		return fmt.Errorf("%w: no doc change was proposed (status %s)", ErrNotEligible, f.Status)
	case f.Status == report.FixVerified:
		return nil
	case f.Status == report.FixBlocked && f.IssuesAfter == 0:
		return nil
	case allowPartial && (f.Status == report.FixImproved || f.Status == report.FixBlocked):
		return nil
	case f.Status == report.FixImproved || f.Status == report.FixBlocked:
		return fmt.Errorf("%w: the fix is only partial (%s, doc issues %d → %d); pass --allow-partial to propose it anyway", ErrNotEligible, f.Status, f.IssuesBefore, f.IssuesAfter)
	default:
		return fmt.Errorf("%w: the fix was not verified (status %s)", ErrNotEligible, f.Status)
	}
}

// Build assembles the proposal. verify may be nil.
func Build(run, verify *report.Run, original, fixed []byte) *Proposal {
	f := run.Fix
	short := run.ID
	if i := strings.LastIndex(short, "-"); i >= 0 {
		short = short[i+1:]
	}
	p := &Proposal{
		Repo:       run.Repo,
		BaseCommit: run.BaseCommit,
		DocPath:    run.DocPath,
		Original:   original,
		Fixed:      fixed,
		Patch:      f.Patch,
		Branch:     "rtfm/fix-quickstart-" + short,
		Title:      fmt.Sprintf("docs: fix quickstart steps in %s that fail on a fresh machine", run.DocPath),
	}

	var cm strings.Builder
	fmt.Fprintf(&cm, "docs: fix quickstart steps in %s\n\n", run.DocPath)
	for _, c := range f.Changes {
		fmt.Fprintf(&cm, "- %s\n", oneLine(c.Summary))
	}
	cm.WriteString("\nFound and verified by rtfm.run, which follows quickstarts on a fresh machine.\n")
	p.CommitMessage = cm.String()
	p.Body = body(run, verify)
	return p
}

func body(run, verify *report.Run) string {
	f := run.Fix
	var b strings.Builder
	fmt.Fprintf(&b, "## Quickstart fixes from a fresh-machine test\n\n")
	fmt.Fprintf(&b, "An AI agent followed `%s` exactly as written, the way a new developer would, on %s. Goal: %s.\n\n",
		run.DocPath, environment(run.Sandbox), strings.TrimSuffix(run.Goal, "."))

	b.WriteString("### What went wrong\n\n| Step | The doc says | What happened |\n|---|---|---|\n")
	for _, s := range run.Steps {
		if s.Status != report.StatusFail && s.Status != report.StatusUnsupported {
			continue
		}
		fmt.Fprintf(&b, "| %d | %s | %s |\n", s.Index, cell(code(firstNonEmpty(s.DocCommand, s.Instruction))), cell(clean(whatHappened(run, s))))
	}

	b.WriteString("\n### Changes\n\n")
	for _, c := range f.Changes {
		fmt.Fprintf(&b, "- **Step %d:** %s\n", c.StepIndex, clean(c.Summary))
		if c.Evidence != "" {
			fmt.Fprintf(&b, "  - Evidence: %s\n", clean(c.Evidence))
		}
	}
	if len(f.Unresolved) > 0 {
		b.WriteString("\nNot changed (needs a maintainer's judgment):\n")
		for _, u := range f.Unresolved {
			fmt.Fprintf(&b, "- Step %d: %s\n", u.StepIndex, clean(u.Reason))
		}
	}

	b.WriteString("\n### Verification\n\n")
	switch {
	case f.Status == report.FixVerified:
		b.WriteString("A second agent then followed the **updated** doc on another fresh machine and reached the goal with no problems. ✅\n")
	case f.BlockedAfter > 0 && f.IssuesAfter == 0:
		fmt.Fprintf(&b, "A second agent followed the **updated** doc on another fresh machine: every documentation problem above is resolved. %d step(s) could not be checked end to end because they need %s, which wasn't available to the test.\n",
			f.BlockedAfter, strings.Join(f.BlockedOn, ", "))
	default:
		fmt.Fprintf(&b, "A second agent followed the **updated** doc on another fresh machine: documentation problems went from %d to %d.\n", f.IssuesBefore, f.IssuesAfter)
	}

	b.WriteString("\n<details><summary>How this was tested</summary>\n\n")
	if run.BaseCommit != "" {
		fmt.Fprintf(&b, "- Tested commit: `%s`\n", run.BaseCommit)
	}
	fmt.Fprintf(&b, "- Environment: %s\n", environment(run.Sandbox))
	if run.Model != "" {
		fmt.Fprintf(&b, "- Agent model: %s\n", run.Model)
	}
	b.WriteString("- The agent only had the docs to go on; when it needed outside knowledge to get past a step, that step counts as a documentation problem.\n")
	if verify != nil {
		pass, _, _ := verify.Counts()
		fmt.Fprintf(&b, "- Verification run: %d steps passed", pass)
		if verify.SuccessCheck != nil {
			fmt.Fprintf(&b, "; success check `%s` exited %d", verify.SuccessCheck.Line, verify.SuccessCheck.ExitCode)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n</details>\n\n---\n")
	b.WriteString("_This pull request was prepared by rtfm.run, which tests quickstarts automatically. Please review it like any other contribution; if something here is wrong, a comment saying so helps us improve._\n")
	return b.String()
}

// whatHappened leads with the real error, then what got past the step.
func whatHappened(run *report.Run, s report.Step) string {
	var parts []string
	if e := fix.FirstError(run, s); e != "" {
		parts = append(parts, "Fails with "+code(e)+".")
	}
	worked := report.NormalizeCommand(s.Command)
	if worked != "" && worked != report.NormalizeCommand(s.DocCommand) {
		parts = append(parts, "It worked as "+code(worked)+".")
	}
	if len(parts) == 0 || s.Failure == report.KindDocGap {
		parts = append(parts, s.Diagnosis)
	}
	return strings.Join(parts, " ")
}

// clean collapses whitespace and turns sandbox paths into repo paths.
func clean(s string) string {
	s = strings.ReplaceAll(s, devboxProject+"/", "")
	s = strings.ReplaceAll(s, devboxProject, "the repository root")
	return oneLine(s)
}

// devboxProject is where the project lives inside a devbox.
const devboxProject = "/work/project"

func environment(sandbox string) string {
	switch {
	case strings.HasPrefix(sandbox, "devbox:firecracker"):
		return "a fresh Ubuntu 24.04 microVM with git, build tools, Python 3, Node.js, Go and Docker, and nothing project-specific installed"
	case strings.HasPrefix(sandbox, "devbox:"):
		return "a fresh Ubuntu 24.04 container with git, build tools, Python 3, Node.js and Go, and nothing project-specific installed"
	default:
		return "a fresh checkout"
	}
}

// ErrDocChanged means upstream's doc no longer matches what was tested.
var ErrDocChanged = errors.New("the doc has changed upstream since it was tested; rerun rtfm on the current version")

// Opened describes a created PR.
type Opened struct {
	URL    string
	Fork   string // owner/name if a fork was used
	Branch string
}

// Open creates the branch, commits the fixed doc and opens the PR. It
// forks the repository when the user can't push to it.
func Open(ctx context.Context, gh *github.Client, p *Proposal) (*Opened, error) {
	up, ok := github.ParseRepo(p.Repo)
	if !ok {
		return nil, fmt.Errorf("%s is not a GitHub repository; use the patch file instead", p.Repo)
	}
	info, err := gh.Repo(ctx, up)
	if err != nil {
		return nil, fmt.Errorf("look up %s: %w", up, err)
	}
	base := info.DefaultBranch
	if p.BaseBranch != "" {
		base = p.BaseBranch
	}

	current, _, err := gh.File(ctx, up, p.DocPath, base)
	if err != nil {
		return nil, fmt.Errorf("read %s on %s: %w", p.DocPath, base, err)
	}
	if string(current) != string(p.Original) {
		return nil, ErrDocChanged
	}
	upSHA, err := gh.BranchSHA(ctx, up, base)
	if err != nil {
		return nil, err
	}

	target, head := up, p.Branch
	opened := &Opened{Branch: p.Branch}
	if !info.Permissions.Push {
		fork, err := gh.Fork(ctx, up)
		if err != nil {
			return nil, fmt.Errorf("fork %s: %w", up, err)
		}
		_ = gh.SyncFork(ctx, fork, base) // best effort; the branch is created from upstream's commit below
		target, head = fork, fork.Owner+":"+p.Branch
		opened.Fork = fork.String()
	}
	if err := gh.CreateBranch(ctx, target, p.Branch, upSHA); err != nil {
		return nil, fmt.Errorf("create branch %s on %s: %w", p.Branch, target, err)
	}
	onBranch, blob, err := gh.File(ctx, target, p.DocPath, p.Branch)
	if err != nil {
		return nil, err
	}
	if string(onBranch) != string(p.Original) {
		return nil, ErrDocChanged
	}
	if err := gh.UpdateFile(ctx, target, p.DocPath, p.Branch, blob, p.CommitMessage, p.Fixed, p.Author); err != nil {
		return nil, fmt.Errorf("commit %s: %w", p.DocPath, err)
	}
	pr, err := gh.CreatePR(ctx, up, p.Title, p.Body, head, base)
	if err != nil {
		return nil, fmt.Errorf("open pull request: %w", err)
	}
	opened.URL = pr.HTMLURL
	return opened, nil
}

func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(s, "`", "'") + "`"
}

func cell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
	if r := []rune(s); len(r) > 300 {
		s = string(r[:297]) + "..."
	}
	return s
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
