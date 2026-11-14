// Package persona holds the newhire agent prompt and its output schema.
package persona

import (
	_ "embed"
	"fmt"
	"strings"
	"text/template"
)

//go:embed newhire.md
var newhireTmpl string

//go:embed report.schema.json
var reportSchema string

var tmpl = template.Must(template.New("newhire").Parse(newhireTmpl))

// Input parameterizes the newhire prompt.
type Input struct {
	Project string // checkout directory, relative to the agent's cwd (e.g. "project")
	DocPath string // path to the quickstart doc, relative to the checkout
	Goal    string // what "done" means, in plain words
	Devbox  bool   // commands run via the devbox MCP exec tool
	// Credentials: the devbox request_credential tool is available.
	Credentials bool
}

// Render returns the newhire prompt for a target.
func Render(in Input) (string, error) {
	if in.DocPath == "" {
		return "", fmt.Errorf("persona: DocPath is required")
	}
	if in.Project == "" {
		in.Project = "."
	}
	if in.Goal == "" {
		in.Goal = "Complete the quickstart exactly as documented."
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, in); err != nil {
		return "", err
	}
	return b.String(), nil
}

// Schema is the JSON schema passed to `agy --json-schema` to force the final
// answer into a report.
func Schema() string { return reportSchema }

//go:embed fixer.md
var fixerTmplText string

//go:embed fixer.schema.json
var fixerSchema string

var fixerTmpl = template.Must(template.New("fixer").Parse(fixerTmplText))

// AppliedFix is a substitution already made from run evidence.
type AppliedFix struct {
	StepIndex int
	Original  string
	Proposed  string
}

// FixerInput parameterizes the fixer prompt.
type FixerInput struct {
	Project     string
	DocPath     string
	Problems    string // rendered newcomer problems (fix.Problems)
	Applied     []AppliedFix
	Devbox      bool
	Credentials bool
}

// RenderFixer returns the docs-maintainer prompt.
func RenderFixer(in FixerInput) (string, error) {
	if in.DocPath == "" || in.Problems == "" {
		return "", fmt.Errorf("persona: fixer needs DocPath and Problems")
	}
	if in.Project == "" {
		in.Project = "."
	}
	var b strings.Builder
	if err := fixerTmpl.Execute(&b, in); err != nil {
		return "", err
	}
	return b.String(), nil
}

// FixerSchema is the JSON schema for the fixer's final report.
func FixerSchema() string { return fixerSchema }

//go:embed guidelines.md
var guidelinesTmplText string

//go:embed guidelines.schema.json
var guidelinesSchema string

var guidelinesTmpl = template.Must(template.New("guidelines").Parse(guidelinesTmplText))

// GuidelineFile is one guideline document from the target repository.
type GuidelineFile struct {
	Path    string
	Content string
}

// GuidelinesInput parameterizes the guidelines prompt.
type GuidelinesInput struct {
	Repo, DocPath                      string
	Title, CommitMessage, Branch, Body string
	Patch                              string
	Files                              []GuidelineFile
}

// RenderGuidelines returns the prompt that applies a repository's
// contribution guidelines to a draft pull request.
func RenderGuidelines(in GuidelinesInput) (string, error) {
	if len(in.Files) == 0 {
		return "", fmt.Errorf("persona: no guideline files")
	}
	var b strings.Builder
	if err := guidelinesTmpl.Execute(&b, in); err != nil {
		return "", err
	}
	return b.String(), nil
}

// GuidelinesSchema is the JSON schema for the guidelines decision.
func GuidelinesSchema() string { return guidelinesSchema }
