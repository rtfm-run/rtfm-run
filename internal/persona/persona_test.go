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
