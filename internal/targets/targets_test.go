package targets

import "testing"

func TestSlug(t *testing.T) {
	cases := []struct {
		t    Target
		want string
	}{
		{Target{Repo: "https://github.com/google-gemini/cookbook.git", DocPath: "quickstarts/Get_started.ipynb"}, "google-gemini-cookbook-get-started"},
		{Target{Repo: "https://github.com/google/adk-samples", DocPath: "README.md"}, "google-adk-samples"},
		{Target{Name: "My Target!", Repo: "x", DocPath: "y"}, "my-target"},
		{Target{Name: "../../etc", Repo: "x", DocPath: "y"}, "etc"},
	}
	for _, c := range cases {
		if got := c.t.Slug(); got != c.want {
			t.Errorf("Slug(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []Target{
		{DocPath: "README.md"},
		{Repo: "x"},
		{Repo: "x", DocPath: "/etc/passwd"},
		{Repo: "x", DocPath: "../secret.md"},
	}
	for _, b := range bad {
		if b.Validate() == nil {
			t.Errorf("Validate(%+v) = nil, want error", b)
		}
	}
	if err := (Target{Repo: "x", DocPath: "docs/quickstart.md"}).Validate(); err != nil {
		t.Error(err)
	}
}
