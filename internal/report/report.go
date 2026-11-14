// Package report defines the result of a newhire run: the steps an agent
// attempted while following a quickstart, what broke, and the evidence.
package report

import (
	"fmt"
	"strings"
	"time"
)

// Status is the outcome of a single quickstart step.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
	// StatusUnsupported means the agent got past the step only by filling a
	// gap from prior knowledge: the docs did not say how. A human newcomer
	// would likely get stuck here.
	StatusUnsupported Status = "unsupported"
)

// FailureKind classifies why a step broke.
type FailureKind string

const (
	KindNone          FailureKind = ""
	KindStaleCommand  FailureKind = "stale_command"
	KindMissingPrereq FailureKind = "missing_prereq"
	KindDeprecatedAPI FailureKind = "deprecated_api"
	KindDocGap        FailureKind = "doc_gap"
	KindBrokenLink    FailureKind = "broken_link"
	KindEnvironment   FailureKind = "environment"
	KindUnknown       FailureKind = "unknown"
)

// Step is one instruction from the docs and what happened when the agent
// followed it, as reported by the agent.
type Step struct {
	Index       int    `json:"index"`
	DocRef      string `json:"doc_ref,omitempty"`
	Instruction string `json:"instruction"`
	// DocCommand is the command exactly as the doc wrote it; Command is what
	// the agent finally ran. When they differ on a step that passed only with
	// outside knowledge, the difference is the doc fix.
	DocCommand string      `json:"doc_command,omitempty"`
	Command    string      `json:"command,omitempty"`
	ExitCode   int         `json:"exit_code"`
	Output     string      `json:"output,omitempty"`
	Status     Status      `json:"status"`
	Note       string      `json:"note,omitempty"`
	Failure    FailureKind `json:"failure,omitempty"`
	Diagnosis  string      `json:"diagnosis,omitempty"`
}

// Command is a shell command observed in the agent's tool-call stream.
// This is ground-truth evidence, independent of the agent's self-report.
type Command struct {
	Line     string `json:"line"`
	ExitCode int    `json:"exit_code"` // -1 if unknown
	Output   string `json:"output,omitempty"`
}

// Usage is token accounting for the run.
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	ThinkingTokens  int `json:"thinking_tokens"`
	CacheReadTokens int `json:"cache_read_tokens"`
	TotalTokens     int `json:"total_tokens"`
}

// Run is a full newhire attempt at one target.
type Run struct {
	ID             string    `json:"id"`
	Target         string    `json:"target"`
	Repo           string    `json:"repo"`
	DocPath        string    `json:"doc_path"`
	Goal           string    `json:"goal"`
	Runner         string    `json:"runner"`
	Model          string    `json:"model,omitempty"`
	Sandbox        string    `json:"sandbox"` // "devbox:<image>" or "host"
	ConversationID string    `json:"conversation_id,omitempty"`
	Started        time.Time `json:"started"`
	Ended          time.Time `json:"ended"`

	// Agent self-report (from --json-schema structured output).
	Succeeded    bool   `json:"succeeded"`
	GaveUpReason string `json:"gave_up_reason,omitempty"`
	Steps        []Step `json:"steps"`

	// Evidence scraped from the stream.
	Commands []Command `json:"commands,omitempty"`
	Searches []string  `json:"searches,omitempty"` // web searches: the agent Googling like a newbie
	URLsRead []string  `json:"urls_read,omitempty"`

	// SuccessCheck is the result of the target's ground-truth command, if any.
	SuccessCheck *Command `json:"success_check,omitempty"`

	// Credentials the agent requested (names and outcomes only, no values).
	Credentials []Credential `json:"credentials,omitempty"`

	// Fix is the proposed doc fix and its verification, if fixing ran.
	Fix *FixResult `json:"fix,omitempty"`

	Usage Usage `json:"usage"`
	// Error is set when the run itself failed (runner crash, timeout), as
	// opposed to the quickstart failing.
	Error string `json:"error,omitempty"`
}

// Duration is wall-clock time for the run.
func (r *Run) Duration() time.Duration { return r.Ended.Sub(r.Started) }

// Works is the overall verdict: the agent says it reached the goal, and the
// ground-truth success check (when configured) agrees.
func (r *Run) Works() bool {
	if r.Error != "" || !r.Succeeded {
		return false
	}
	if r.SuccessCheck != nil && r.SuccessCheck.ExitCode != 0 {
		return false
	}
	return true
}

// Counts tallies step outcomes.
func (r *Run) Counts() (pass, fail, unsupported int) {
	for _, s := range r.Steps {
		switch s.Status {
		case StatusPass:
			pass++
		case StatusFail:
			fail++
		case StatusUnsupported:
			unsupported++
		}
	}
	return
}

// Markdown renders a human-readable report.
func (r *Run) Markdown() string {
	var b strings.Builder
	verdict := "❌ broken"
	switch {
	case r.Error != "":
		verdict = "⚠️ run error"
	case r.Works():
		verdict = "✅ works"
	case r.Blocked():
		verdict = "🔑 blocked on credentials (" + strings.Join(r.MissingCredentials(), ", ") + ")"
	case len(r.MissingCredentials()) > 0:
		verdict = "❌ broken (also needs " + strings.Join(r.MissingCredentials(), ", ") + ")"
	}
	pass, fail, unsup := r.Counts()
	fmt.Fprintf(&b, "# vanguard.run · %s\n\n", r.Target)
	fmt.Fprintf(&b, "**Verdict:** %s · **Steps:** %d pass / %d fail / %d unsupported · **Time:** %s · **Model:** %s\n\n",
		verdict, pass, fail, unsup, r.Duration().Round(time.Second), r.Model)
	fmt.Fprintf(&b, "**Goal:** %s · **Docs:** `%s`\n\n", r.Goal, r.DocPath)
	if r.Error != "" {
		fmt.Fprintf(&b, "> Run error: %s\n\n", r.Error)
	}
	if r.GaveUpReason != "" {
		fmt.Fprintf(&b, "> Gave up: %s\n\n", r.GaveUpReason)
	}
	if r.SuccessCheck != nil {
		fmt.Fprintf(&b, "> Success check `%s` → exit %d\n\n", r.SuccessCheck.Line, r.SuccessCheck.ExitCode)
	}
	if len(r.Steps) > 0 {
		b.WriteString("| # | Status | Instruction | Command | Exit | Failure | Note |\n|---|---|---|---|---|---|---|\n")
		for _, s := range r.Steps {
			fmt.Fprintf(&b, "| %d | %s | %s | %s | %d | %s | %s |\n",
				s.Index, s.Status, cell(s.Instruction), stepCommand(s), s.ExitCode, s.Failure, cell(firstNonEmpty(s.Diagnosis, s.Note)))
		}
		b.WriteString("\n")
	}
	if len(r.Credentials) > 0 {
		b.WriteString("**Credentials requested:**\n")
		for _, c := range r.Credentials {
			fmt.Fprintf(&b, "- `%s`: %s", c.Name, c.Status)
			if c.Source != "" {
				fmt.Fprintf(&b, " (from %s)", c.Source)
			}
			if c.Description != "" {
				fmt.Fprintf(&b, " · %s", c.Description)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(r.Searches) > 0 {
		b.WriteString("**Googled:**\n")
		for _, q := range r.Searches {
			fmt.Fprintf(&b, "- %s\n", q)
		}
		b.WriteString("\n")
	}
	if r.Fix != nil {
		b.WriteString(r.Fix.Markdown())
	}
	fmt.Fprintf(&b, "_%d commands observed · %d tokens · conversation %s_\n", len(r.Commands), r.Usage.TotalTokens, r.ConversationID)
	return b.String()
}

// stepCommand shows what ran, and what the doc said when that differs.
func stepCommand(s Step) string {
	if s.DocCommand != "" && s.Command != "" && NormalizeCommand(s.DocCommand) != NormalizeCommand(s.Command) {
		return "doc: " + code(s.DocCommand) + " → ran: " + code(s.Command)
	}
	return code(firstNonEmpty(s.Command, s.DocCommand))
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func code(s string) string {
	if s == "" {
		return ""
	}
	return "`" + strings.ReplaceAll(cell(s), "`", "'") + "`"
}

func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:97]) + "..."
	}
	return s
}

// Issues counts steps a newcomer would trip on: failed or unsupported.
func (r *Run) Issues() int {
	_, fail, unsup := r.Counts()
	return fail + unsup
}

// CredentialBlockedSteps counts issue steps that are credential/config
// failures while a requested credential was unavailable: they can't be
// judged until the credential is provided.
func (r *Run) CredentialBlockedSteps() int {
	if len(r.MissingCredentials()) == 0 {
		return 0
	}
	n := 0
	for _, s := range r.Steps {
		if (s.Status == StatusFail || s.Status == StatusUnsupported) && s.Failure == KindEnvironment {
			n++
		}
	}
	return n
}

// DocIssues counts issues that are not just waiting on a credential.
func (r *Run) DocIssues() int { return r.Issues() - r.CredentialBlockedSteps() }

// LastCommand returns the last observed command whose line matches line
// (whitespace-insensitive), or nil.
func (r *Run) LastCommand(line string) *Command {
	want := NormalizeCommand(line)
	if want == "" {
		return nil
	}
	for i := len(r.Commands) - 1; i >= 0; i-- {
		if NormalizeCommand(r.Commands[i].Line) == want {
			return &r.Commands[i]
		}
	}
	return nil
}

// Verification outcomes for a fix.
const (
	FixVerified   = "verified"   // rerun with the fixed doc works with zero issues
	FixImproved   = "improved"   // rerun has fewer issues than the original
	FixNotFixed   = "not_fixed"  // rerun is no better
	FixNoChange   = "no_change"  // no doc change was proposed
	FixUnverified = "unverified" // a fix was proposed but not rerun
	FixBlocked    = "blocked"    // the rerun needed credentials that weren't available
	FixError      = "error"      // fixing or verifying failed to run
)

// Change is one doc change and why it was made.
type Change struct {
	StepIndex int    `json:"step_index"`
	Summary   string `json:"summary"`
	Evidence  string `json:"evidence,omitempty"`
	// Source is "run" (the command that actually worked replaced the
	// documented one) or "fixer" (the fixer agent's investigation).
	Source string `json:"source"`
}

// Unresolved is a problem the fixer could not fix in the docs.
type Unresolved struct {
	StepIndex int    `json:"step_index"`
	Reason    string `json:"reason"`
}

// FixResult is a proposed doc fix, proven (or not) by rerunning the
// quickstart with it in a fresh devbox.
type FixResult struct {
	DocPath    string       `json:"doc_path"`
	Patch      string       `json:"patch,omitempty"` // unified diff of the doc
	Changes    []Change     `json:"changes,omitempty"`
	Unresolved []Unresolved `json:"unresolved,omitempty"`

	Status       string `json:"status"`
	IssuesBefore int    `json:"issues_before"`
	// IssuesAfter counts doc issues in the verify run; BlockedAfter counts
	// its steps that only failed because a credential was unavailable.
	IssuesAfter    int      `json:"issues_after"`
	BlockedAfter   int      `json:"blocked_after,omitempty"`
	BlockedOn      []string `json:"blocked_on,omitempty"`
	VerifyRunID    string   `json:"verify_run_id,omitempty"`
	ConversationID string   `json:"fixer_conversation_id,omitempty"`
	Usage          Usage    `json:"usage"` // fixer + verify tokens
	Error          string   `json:"error,omitempty"`
}

// Markdown renders the fix section of a run report.
func (f *FixResult) Markdown() string {
	var b strings.Builder
	label := map[string]string{
		FixVerified:   "✅ verified: a fresh newcomer succeeded with the fixed doc",
		FixImproved:   "🟡 improved: fewer issues with the fixed doc",
		FixNotFixed:   "❌ not fixed: the rerun was no better",
		FixNoChange:   "➖ no doc change proposed",
		FixUnverified: "🔎 proposed, not verified (run with --verify)",
		FixBlocked:    "🔑 proposed, verification blocked on credentials",
		FixError:      "⚠️ fix run error",
	}[f.Status]
	fmt.Fprintf(&b, "## Fix · %s\n\n", label)
	if f.VerifyRunID != "" {
		fmt.Fprintf(&b, "Doc issues: %d → %d", f.IssuesBefore, f.IssuesAfter)
		if f.BlockedAfter > 0 {
			fmt.Fprintf(&b, " · %d step(s) blocked on %s", f.BlockedAfter, strings.Join(f.BlockedOn, ", "))
		}
		fmt.Fprintf(&b, " (verify run `%s`)\n\n", f.VerifyRunID)
	}
	if f.Error != "" {
		fmt.Fprintf(&b, "> %s\n\n", f.Error)
	}
	for _, c := range f.Changes {
		fmt.Fprintf(&b, "- **Step %d** (%s): %s", c.StepIndex, c.Source, c.Summary)
		if c.Evidence != "" {
			fmt.Fprintf(&b, " _Evidence: %s_", c.Evidence)
		}
		b.WriteString("\n")
	}
	for _, u := range f.Unresolved {
		fmt.Fprintf(&b, "- **Step %d** unresolved: %s\n", u.StepIndex, u.Reason)
	}
	if len(f.Changes)+len(f.Unresolved) > 0 {
		b.WriteString("\n")
	}
	if f.Patch != "" {
		fmt.Fprintf(&b, "```diff\n%s\n```\n\n", strings.TrimRight(f.Patch, "\n"))
	}
	return b.String()
}

// Credential is a credential the agent requested during a run. It never
// contains the value.
type Credential struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	Status      string `json:"status"`           // provided | unavailable | invalid
	Source      string `json:"source,omitempty"` // store | prompt
}

// MissingCredentials lists credentials that were requested but not provided.
func (r *Run) MissingCredentials() []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range r.Credentials {
		if c.Status != "provided" && c.Status != "invalid" && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c.Name)
		}
	}
	return out
}

// Blocked reports whether the only thing keeping the run from finishing is
// credentials that weren't available: every issue is a credential/config
// (environment) issue and some requested credential is missing. A run that
// also has genuine doc problems is broken, not blocked.
func (r *Run) Blocked() bool {
	if r.Error != "" || r.Works() || len(r.MissingCredentials()) == 0 {
		return false
	}
	for _, s := range r.Steps {
		if (s.Status == StatusFail || s.Status == StatusUnsupported) && s.Failure != KindEnvironment {
			return false
		}
	}
	return true
}

// NormalizeCommand collapses whitespace and strips a leading shell prompt
// ("$ ", "% ", "> ") so a doc's `$ pip install x` matches `pip install x`.
func NormalizeCommand(s string) string {
	return strings.Join(strings.Fields(StripPrompt(s)), " ")
}

// StripPrompt removes a leading shell prompt marker.
func StripPrompt(s string) string {
	t := strings.TrimLeft(s, " \t")
	for _, p := range []string{"$ ", "% ", "> "} {
		if strings.HasPrefix(t, p) {
			return t[len(p):]
		}
	}
	return t
}
