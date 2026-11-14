// Package targets describes quickstarts to test.
package targets

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// Target is one quickstart.
type Target struct {
	// Name is a short unique slug; derived from Repo+DocPath if empty.
	Name string `json:"name,omitempty"`
	// Repo is a git URL or a local directory.
	Repo string `json:"repo"`
	Ref  string `json:"ref,omitempty"`
	// DocPath is the quickstart doc, relative to the repo root.
	DocPath string `json:"doc"`
	// Goal says what "done" means, e.g. "print a reply from the Gemini API".
	Goal string `json:"goal"`
	// Success is an optional shell command run in the workspace after the
	// agent finishes; exit 0 means the goal was really reached.
	Success string `json:"success,omitempty"`
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// Slug returns a filesystem- and URL-safe identifier.
func (t Target) Slug() string {
	if t.Name != "" {
		return Sanitize(t.Name)
	}
	r := strings.TrimSuffix(t.Repo, ".git")
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimPrefix(r, "http://")
	r = strings.TrimPrefix(r, "github.com/")
	doc := strings.TrimSuffix(path.Base(t.DocPath), path.Ext(t.DocPath))
	if doc != "" && !strings.EqualFold(doc, "readme") {
		r += "-" + doc
	}
	return Sanitize(r)
}

// Sanitize lowercases s and collapses anything outside [a-z0-9] to '-'.
func Sanitize(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "target"
	}
	return s
}

// Validate checks required fields.
func (t Target) Validate() error {
	if t.Repo == "" {
		return fmt.Errorf("target %q: repo is required", t.Name)
	}
	if t.DocPath == "" {
		return fmt.Errorf("target %q: doc is required", t.Name)
	}
	if strings.HasPrefix(t.DocPath, "/") || strings.Contains(t.DocPath, "..") {
		return fmt.Errorf("target %q: doc must be a relative path inside the repo", t.Name)
	}
	return nil
}

// Load reads a JSON array of targets.
func Load(file string) ([]Target, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var ts []Target
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, fmt.Errorf("targets: %s: %w", file, err)
	}
	seen := map[string]bool{}
	for i, t := range ts {
		if err := t.Validate(); err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		if seen[t.Slug()] {
			return nil, fmt.Errorf("targets[%d]: duplicate name %q", i, t.Slug())
		}
		seen[t.Slug()] = true
	}
	return ts, nil
}
