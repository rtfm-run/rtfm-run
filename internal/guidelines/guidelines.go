// Package guidelines makes vanguard's pull requests follow the target
// repository's contribution rules: PR templates, CONTRIBUTING, commit
// conventions, DCO/CLA requirements and AI-contribution policies.
//
// An agent interprets the documents (they are prose); the enforcement is
// deterministic: a ban on AI contributions is a hard stop, and anything that
// only the human submitting the PR can attest to (sign-off, CLA, "I have
// read the guidelines") requires an explicit flag from that human.
package guidelines

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"vanguard.run/internal/github"
	"vanguard.run/internal/persona"
	"vanguard.run/internal/prs"
	"vanguard.run/internal/runner"
)

// File is one guideline document.
type File struct {
	Path    string
	Content string
}

// Found is what a repository says about contributing.
type Found struct {
	Templates    []File
	Contributing []File
	Conventions  []File // commitlint configs, .gitmessage, AI policies
	DCOConfig    bool   // .github/dco.yml present
}

// All returns every discovered file.
func (f *Found) All() []File {
	var all []File
	all = append(all, f.Templates...)
	all = append(all, f.Contributing...)
	return append(all, f.Conventions...)
}

// Empty reports whether nothing was found.
func (f *Found) Empty() bool { return len(f.All()) == 0 && !f.DCOConfig }

var (
	templateRe     = regexp.MustCompile(`(?i)^pull_request_template(\.(md|markdown|txt))?$`)
	templateDirRe  = regexp.MustCompile(`(?i)^pull_request_template$`)
	contributingRe = regexp.MustCompile(`(?i)^contributing(\.(md|markdown|rst|txt|adoc))?$`)
	conventionRe   = regexp.MustCompile(`(?i)^(\.commitlintrc(\..+)?|commitlint\.config\.(js|cjs|mjs|ts|json|ya?ml)|\.gitmessage|ai[_-]?(policy|contributions?|usage)(\.(md|txt))?)$`)
	dcoRe          = regexp.MustCompile(`(?i)^dco\.ya?ml$`)
)

// maxFile caps how much of each document is used.
const maxFile = 30 << 10

// Discover lists the repository root, .github/ and docs/ at ref and loads
// the contribution documents found there (names matched case-insensitively,
// as GitHub does).
func Discover(ctx context.Context, gh *github.Client, repo github.Repo, ref string) (*Found, error) {
	f := &Found{}
	load := func(path string) (File, error) {
		b, _, err := gh.File(ctx, repo, path, ref)
		if err != nil {
			return File{}, err
		}
		s := string(b)
		if len(s) > maxFile {
			s = s[:maxFile] + "\n…(truncated)"
		}
		return File{Path: path, Content: s}, nil
	}
	for _, dir := range []string{"", ".github", "docs"} {
		entries, err := gh.ListDir(ctx, repo, dir, ref)
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", dir, err)
		}
		for _, e := range entries {
			switch {
			case e.Type == "file" && templateRe.MatchString(e.Name):
				if file, err := load(e.Path); err == nil {
					f.Templates = append(f.Templates, file)
				}
			case e.Type == "dir" && templateDirRe.MatchString(e.Name):
				sub, err := gh.ListDir(ctx, repo, e.Path, ref)
				if err != nil {
					return nil, err
				}
				for _, s := range sub {
					if s.Type == "file" && (strings.HasSuffix(strings.ToLower(s.Name), ".md") || strings.HasSuffix(strings.ToLower(s.Name), ".txt")) {
						if file, err := load(s.Path); err == nil {
							f.Templates = append(f.Templates, file)
						}
					}
				}
			case e.Type == "file" && contributingRe.MatchString(e.Name):
				if file, err := load(e.Path); err == nil {
					f.Contributing = append(f.Contributing, file)
				}
			case e.Type == "file" && conventionRe.MatchString(e.Name):
				if file, err := load(e.Path); err == nil {
					f.Conventions = append(f.Conventions, file)
				}
			case dir == ".github" && e.Type == "file" && dcoRe.MatchString(e.Name):
				f.DCOConfig = true
				if file, err := load(e.Path); err == nil {
					f.Conventions = append(f.Conventions, file)
				}
			}
		}
	}
	return f, nil
}

// Decision is the agent's reading of the guidelines, applied to the draft.
type Decision struct {
	AIPolicy struct {
		Status       string `json:"status"` // allowed | restricted | forbidden | not_mentioned
		Quote        string `json:"quote"`
		Requirements string `json:"requirements"`
	} `json:"ai_policy"`
	IssueFirst struct {
		Required bool   `json:"required"`
		Quote    string `json:"quote"`
	} `json:"issue_first"`
	DCORequired   bool   `json:"dco_required"`
	CLARequired   bool   `json:"cla_required"`
	TargetBranch  string `json:"target_branch"`
	Title         string `json:"title"`
	CommitSubject string `json:"commit_subject"`
	CommitBody    string `json:"commit_body"`
	BranchName    string `json:"branch_name"`
	TemplateUsed  string `json:"template_used"`
	Body          string `json:"body"`
	Checkboxes    []struct {
		Text       string `json:"text"`
		Checked    bool   `json:"checked"`
		NeedsHuman bool   `json:"needs_human"`
		Reason     string `json:"reason"`
	} `json:"checkboxes"`
	OtherRequirements []struct {
		Requirement string `json:"requirement"`
		Satisfied   bool   `json:"satisfied"`
		How         string `json:"how"`
	} `json:"other_requirements"`
	Notes []string `json:"notes"`
}

// Key identifies the inputs of an interpretation: the guideline files, the
// draft and the model. A saved decision is reused only for the same key, so
// --open posts exactly what the dry run showed.
func Key(f *Found, p *prs.Proposal, model string) string {
	h := sha256.New()
	for _, file := range f.All() {
		fmt.Fprintf(h, "%s\x00%s\x00", file.Path, file.Content)
	}
	fmt.Fprintf(h, "%v\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", f.DCOConfig, p.Title, p.CommitMessage, p.Branch, p.Body, p.Patch, model)
	return hex.EncodeToString(h.Sum(nil))
}

type saved struct {
	Key      string    `json:"key"`
	Decision *Decision `json:"decision"`
}

// Load returns a saved decision for key, if there is one.
func Load(path, key string) *Decision {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s saved
	if json.Unmarshal(b, &s) != nil || s.Key != key {
		return nil
	}
	return s.Decision
}

// Save stores a decision under key.
func Save(path, key string, d *Decision) error {
	b, err := json.MarshalIndent(saved{Key: key, Decision: d}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// Interpret has an agent (with every tool denied) read the guidelines and
// produce a decision for the draft proposal.
func Interpret(ctx context.Context, r runner.Runner, repo string, f *Found, p *prs.Proposal) (*Decision, error) {
	in := persona.GuidelinesInput{Repo: repo, DocPath: p.DocPath, Title: p.Title, CommitMessage: p.CommitMessage,
		Branch: p.Branch, Body: p.Body, Patch: p.Patch}
	for _, file := range f.All() {
		in.Files = append(in.Files, persona.GuidelineFile{Path: file.Path, Content: file.Content})
	}
	if f.DCOConfig {
		in.Files = append(in.Files, persona.GuidelineFile{Path: ".github/dco.yml (present)", Content: "The repository has a DCO app configuration: commits need Signed-off-by lines."})
	}
	prompt, err := persona.RenderGuidelines(in)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "vanguard-guidelines-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	sum, err := r.RunPrompt(ctx, runner.Request{Workdir: dir, NoTools: true}, prompt, persona.GuidelinesSchema())
	if err != nil {
		return nil, fmt.Errorf("interpret guidelines: %w", err)
	}
	if sum == nil || sum.Result == nil || len(sum.Result.StructuredOutput) == 0 {
		return nil, fmt.Errorf("interpret guidelines: no decision returned")
	}
	var d Decision
	if err := json.Unmarshal(sum.Result.StructuredOutput, &d); err != nil {
		return nil, fmt.Errorf("interpret guidelines: %w", err)
	}
	return &d, nil
}

// Options are the submitting human's explicit answers.
type Options struct {
	Signoff   bool             // add Signed-off-by (DCO) as Identity
	Identity  *github.Identity // the human's git identity
	CLASigned bool             // the human has signed the project's CLA
	Attest    bool             // the human confirms the personal checklist items
	Issue     string           // issue URL to reference
}

// Blocker stops --open until resolved. Hard blockers can't be overridden.
type Blocker struct {
	Message string
	Hard    bool
}

// Outcome reports how the guidelines were applied.
type Outcome struct {
	Found    []string
	Applied  []string
	Warnings []string
	Blockers []Blocker
}

// Ready reports whether the PR may be opened.
func (o *Outcome) Ready() bool { return len(o.Blockers) == 0 }

var branchRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)

// disclosure is kept in every body, whatever the template.
const disclosure = "_This change was found and verified by vanguard.run, which had an AI agent follow the quickstart on a fresh machine._"

// Apply mutates p according to the decision and the human's options. It
// never ticks a personal-attestation box or adds a sign-off on its own.
func Apply(p *prs.Proposal, d *Decision, f *Found, opt Options) Outcome {
	var o Outcome
	for _, file := range f.All() {
		o.Found = append(o.Found, file.Path)
	}
	if d == nil {
		return o
	}

	switch d.AIPolicy.Status {
	case "forbidden":
		o.Blockers = append(o.Blockers, Blocker{Hard: true, Message: "The project does not accept AI-generated contributions" + quoted(d.AIPolicy.Quote) + ". vanguard will not open this pull request; the patch and text are still saved locally."})
	case "restricted":
		o.Warnings = append(o.Warnings, "AI-assisted contributions are accepted with conditions: "+d.AIPolicy.Requirements+quoted(d.AIPolicy.Quote)+". Check the PR meets them.")
	}

	if t := strings.TrimSpace(d.Title); t != "" {
		p.Title = t
	}
	if b := strings.TrimSpace(d.Body); b != "" {
		p.Body = b
		if d.TemplateUsed != "" {
			o.Applied = append(o.Applied, "Filled in the pull request template "+d.TemplateUsed)
		}
	}
	if !strings.Contains(p.Body, "vanguard.run") {
		p.Body = strings.TrimRight(p.Body, "\n") + "\n\n" + disclosure + "\n"
	}
	if s := strings.TrimSpace(d.CommitSubject); s != "" {
		p.CommitMessage = s + "\n"
		if body := strings.TrimSpace(stripSignoffs(d.CommitBody)); body != "" {
			p.CommitMessage += "\n" + body + "\n"
		}
	}
	if bn := strings.TrimSpace(d.BranchName); bn != "" {
		if branchRe.MatchString(bn) && !strings.Contains(bn, "..") {
			p.Branch = bn
		} else {
			o.Warnings = append(o.Warnings, fmt.Sprintf("Ignored suggested branch name %q (not a valid branch name); using %s", bn, p.Branch))
		}
	}
	if tb := strings.TrimSpace(d.TargetBranch); tb != "" {
		p.BaseBranch = tb
		o.Applied = append(o.Applied, "Targets branch "+tb+" as the guidelines require")
	}

	if d.IssueFirst.Required {
		if opt.Issue == "" {
			o.Blockers = append(o.Blockers, Blocker{Message: "The guidelines require an issue before a pull request" + quoted(d.IssueFirst.Quote) + ". Open or find one, then pass --issue <url>."})
		} else {
			p.Body = strings.TrimRight(p.Body, "\n") + "\n\nRelated issue: " + opt.Issue + "\n"
			o.Applied = append(o.Applied, "Referenced issue "+opt.Issue)
		}
	}

	if d.DCORequired || f.DCOConfig {
		switch {
		case !opt.Signoff:
			o.Blockers = append(o.Blockers, Blocker{Message: "The project requires a DCO sign-off (Signed-off-by), which certifies the contribution as yours. If you agree, pass --signoff to sign with your git identity."})
		case opt.Identity == nil || opt.Identity.Name == "" || opt.Identity.Email == "":
			o.Blockers = append(o.Blockers, Blocker{Message: "--signoff needs your git identity: set git config user.name and user.email."})
		default:
			p.CommitMessage = strings.TrimRight(p.CommitMessage, "\n") + fmt.Sprintf("\n\nSigned-off-by: %s <%s>\n", opt.Identity.Name, opt.Identity.Email)
			p.Author = opt.Identity
			o.Applied = append(o.Applied, fmt.Sprintf("Signed off (DCO) as %s; the commit is authored by the same identity", opt.Identity.Name))
		}
	}
	if d.CLARequired && !opt.CLASigned {
		o.Blockers = append(o.Blockers, Blocker{Message: "The project requires a signed Contributor License Agreement. Sign it yourself, then pass --cla-signed."})
	}

	var human []string
	for _, c := range d.Checkboxes {
		if !c.NeedsHuman {
			continue
		}
		if !opt.Attest {
			human = append(human, c.Text)
			continue
		}
		if ticked, ok := tick(p.Body, c.Text); ok {
			p.Body = ticked
		} else {
			o.Warnings = append(o.Warnings, "Could not find checklist item to tick: "+c.Text)
		}
	}
	if len(human) > 0 {
		o.Blockers = append(o.Blockers, Blocker{Message: "The template has items only you can confirm; review them and pass --attest if they are true:\n    - " + strings.Join(human, "\n    - ")})
	} else if opt.Attest {
		o.Applied = append(o.Applied, "Ticked the checklist items you attested to")
	}

	for _, r := range d.OtherRequirements {
		if !r.Satisfied {
			o.Warnings = append(o.Warnings, "Not met: "+r.Requirement+" ("+r.How+")")
		}
	}
	o.Applied = append(o.Applied, d.Notes...)
	return o
}

var checkboxRe = regexp.MustCompile(`(?m)^(\s*[-*]\s*)\[ \](\s*)(.*)$`)

// tick checks the unchecked box whose text matches (whitespace-insensitive).
func tick(body, text string) (string, bool) {
	want := strings.Join(strings.Fields(text), " ")
	done := false
	out := checkboxRe.ReplaceAllStringFunc(body, func(line string) string {
		m := checkboxRe.FindStringSubmatch(line)
		if done || strings.Join(strings.Fields(m[3]), " ") != want {
			return line
		}
		done = true
		return m[1] + "[x]" + m[2] + m[3]
	})
	return out, done
}

var signoffRe = regexp.MustCompile(`(?mi)^signed-off-by:.*$\n?`)

func stripSignoffs(s string) string { return signoffRe.ReplaceAllString(s, "") }

func quoted(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	return ` ("` + strings.Join(strings.Fields(q), " ") + `")`
}

// Render formats the outcome for the dry run.
func (o *Outcome) Render() string {
	var b strings.Builder
	if len(o.Found) == 0 {
		b.WriteString("Contribution guidelines: none found\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Contribution guidelines found: %s\n", strings.Join(o.Found, ", "))
	for _, a := range o.Applied {
		fmt.Fprintf(&b, "  ✓ %s\n", a)
	}
	for _, w := range o.Warnings {
		fmt.Fprintf(&b, "  ⚠ %s\n", w)
	}
	for _, bl := range o.Blockers {
		mark := "✗"
		if bl.Hard {
			mark = "⛔"
		}
		fmt.Fprintf(&b, "  %s %s\n", mark, bl.Message)
	}
	return b.String()
}
