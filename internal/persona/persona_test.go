package persona

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	p, err := Render(Input{DocPath: "docs/quickstart.md", Goal: "get a reply from the API"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docs/quickstart.md", "get a reply from the API", "unsupported"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
	if _, err := Render(Input{}); err == nil {
		t.Error("Render without DocPath should fail")
	}
}

func TestSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(Schema()), &v); err != nil {
		t.Fatal(err)
	}
}

func TestRenderFixer(t *testing.T) {
	p, err := RenderFixer(FixerInput{
		Project: "project", DocPath: "README.md", Problems: "Step 4 [fail]: gemini: command not found", Devbox: true,
		Applied: []AppliedFix{{StepIndex: 3, Original: "hello --user x", Proposed: "hello --name x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project/README.md", "gemini: command not found", "`hello --user x` → `hello --name x`", "devbox"} {
		if !strings.Contains(p, want) {
			t.Errorf("fixer prompt missing %q", want)
		}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(FixerSchema()), &v); err != nil {
		t.Fatal(err)
	}
}
