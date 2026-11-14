package match

import "testing"

func TestGlobs(t *testing.T) {
	cases := []struct {
		glob, s string
		want    bool
	}{
		{"README.md", "README.md", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/a.md", false},
		{"docs/**", "docs/a/b.md", true},
		{"**/*.md", "README.md", true},
		{"**/*.md", "a/b/c.md", true},
		{"release/*", "release/1.2", true},
		{"release/*", "release/1/2", false},
		{"v?.?", "v1.2", true},
		{"src/(x).go", "src/(x).go", true},
	}
	for _, c := range cases {
		re, err := Glob(c.glob)
		if err != nil {
			t.Fatal(err)
		}
		if got := re.MatchString(c.s); got != c.want {
			t.Errorf("%q ~ %q = %v, want %v", c.glob, c.s, got, c.want)
		}
	}
}

func TestBranchAndPaths(t *testing.T) {
	if !Branch(nil, "main", "main") || Branch(nil, "dev", "main") {
		t.Error("no globs = only the quickstart's branch")
	}
	if !Branch([]string{"release/*", "main"}, "release/2", "main") {
		t.Error("glob branch")
	}
	if !Paths(nil, []string{"x.go"}, true) {
		t.Error("no path globs = any change")
	}
	if Paths([]string{"README.md", "docs/**"}, []string{"src/main.go"}, true) {
		t.Error("unrelated change matched")
	}
	if !Paths([]string{"docs/**"}, []string{"src/main.go"}, false) {
		t.Error("incomplete file lists must match to be safe")
	}
	if !Paths([]string{"docs/**"}, []string{"docs/guide/install.md"}, true) {
		t.Error("docs change")
	}
}
