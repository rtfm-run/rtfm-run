// Package match decides whether a push should trigger a quickstart run:
// branch and path globs where "*" matches within a path segment and "**"
// matches across segments.
package match

import (
	"regexp"
	"strings"
)

// Glob compiles a glob ("docs/**", "*.md", "release/*") to a regexp.
func Glob(g string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch {
		case c == '*' && i+1 < len(g) && g[i+1] == '*':
			i++
			if i+1 < len(g) && g[i+1] == '/' { // "**/" also matches zero directories
				i++
				b.WriteString("(?:.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// Valid reports whether every glob compiles.
func Valid(globs []string) error {
	for _, g := range globs {
		if _, err := Glob(g); err != nil {
			return err
		}
	}
	return nil
}

func any(globs []string, s string) bool {
	for _, g := range globs {
		if re, err := Glob(g); err == nil && re.MatchString(s) {
			return true
		}
	}
	return false
}

// Branch reports whether a push to branch matches. With no globs, only the
// quickstart's own branch (fallback) matches.
func Branch(globs []string, branch, fallback string) bool {
	if len(globs) == 0 {
		return branch == fallback
	}
	return any(globs, branch)
}

// Paths reports whether changed files match. With no globs any change
// matches. complete=false means the file list may be truncated (e.g. a push
// with many commits), so it matches to be safe.
func Paths(globs, changed []string, complete bool) bool {
	if len(globs) == 0 || !complete {
		return true
	}
	for _, f := range changed {
		if any(globs, f) {
			return true
		}
	}
	return false
}
