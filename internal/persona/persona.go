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
	DocPath string // path to the quickstart doc, relative to the workspace
	Goal    string // what "done" means, in plain words
	Devbox  bool   // commands run via the devbox MCP exec tool
}

// Render returns the newhire prompt for a target.
func Render(in Input) (string, error) {
	if in.DocPath == "" {
		return "", fmt.Errorf("persona: DocPath is required")
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
