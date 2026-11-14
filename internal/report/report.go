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
	KindUndefinedTerm FailureKind = "undefined_term"
	KindBrokenLink    FailureKind = "broken_link"
	KindEnvironment   FailureKind = "environment"
	KindUnknown       FailureKind = "unknown"
)

// Step is one instruction from the docs and what happened when the agent
// followed it, as reported by the agent.
type Step struct {
	Index       int         `json:"index"`
	DocRef      string      `json:"doc_ref,omitempty"`
	Instruction string      `json:"instruction"`
	Command     string      `json:"command,omitempty"`
	ExitCode    int         `json:"exit_code"`
	Output      string      `json:"output,omitempty"`
	Status      Status      `json:"status"`
	Note        string      `json:"note,omitempty"`
	Failure     FailureKind `json:"failure,omitempty"`
	Diagnosis   string      `json:"diagnosis,omitempty"`
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
				s.Index, s.Status, cell(s.Instruction), code(s.Command), s.ExitCode, s.Failure, cell(firstNonEmpty(s.Diagnosis, s.Note)))
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
	fmt.Fprintf(&b, "_%d commands observed · %d tokens · conversation %s_\n", len(r.Commands), r.Usage.TotalTokens, r.ConversationID)
	return b.String()
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
