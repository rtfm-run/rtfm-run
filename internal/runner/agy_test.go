package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: launching a Go test binary as the devbox MCP server re-ran
// the tests inside it, each level starting another microVM.
func TestInstallMCPRefusesTestBinaries(t *testing.T) {
	dir := t.TempDir()
	req := Request{Workdir: dir, BoxBackend: "docker", BoxID: "vg-x", BoxWorkdir: "/work/project"}
	for _, bin := range []string{"/tmp/go-build123/b001/worker.test", "/home/me/pkg.test"} {
		if err := (Agy{SelfBin: bin}).installMCP(req); err == nil || !strings.Contains(err.Error(), "test binary") {
			t.Errorf("SelfBin %s: err = %v", bin, err)
		}
	}
	if err := (Agy{SelfBin: "/usr/local/bin/vanguard"}).installMCP(req); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".agents", "mcp_config.json"))
	var cfg struct {
		MCPServers map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	json.Unmarshal(b, &cfg)
	if cfg.MCPServers["devbox"].Env[MCPChildEnv] != "1" {
		t.Errorf("MCP server must carry the recursion-guard marker: %s", b)
	}
}
