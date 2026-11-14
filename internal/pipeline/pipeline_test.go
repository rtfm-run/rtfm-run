package pipeline

import (
	"context"
	"strings"
	"testing"

	"rtfm.run/internal/runner"
	"rtfm.run/internal/targets"
)

func TestRecursionGuard(t *testing.T) {
	t.Setenv(runner.MCPChildEnv, "1")
	_, err := Execute(context.Background(), Config{Runner: runner.Agy{}}, targets.Target{Repo: "x", DocPath: "README.md", Goal: "g"})
	if err == nil || !strings.Contains(err.Error(), "recursion guard") {
		t.Errorf("err = %v", err)
	}
}
