package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	doc := fs.String("doc", "README.md", "")
	keep := fs.Bool("keep", false, "")

	pos, err := parseInterspersed(fs, []string{"https://x/repo", "--doc", "q.md", "--keep"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pos, []string{"https://x/repo"}) || *doc != "q.md" || !*keep {
		t.Errorf("pos=%v doc=%q keep=%v", pos, *doc, *keep)
	}
}
