package sandbox

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGitAuthEnv(t *testing.T) {
	if env := gitAuthEnv("https://github.com/o/r", ""); env != nil {
		t.Errorf("no token should mean no auth: %v", env)
	}
	if env := gitAuthEnv("https://gitlab.com/o/r", "tok"); env != nil {
		t.Errorf("token must only be sent to github.com: %v", env)
	}
	env := gitAuthEnv("https://github.com/o/r", "ghp_x")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://github.com/.extraheader") ||
		!strings.Contains(joined, base64.StdEncoding.EncodeToString([]byte("x-access-token:ghp_x"))) {
		t.Errorf("env = %v", env)
	}
	if strings.Contains(joined, "ghp_x") {
		t.Error("raw token should only appear base64-encoded in the header")
	}
}
