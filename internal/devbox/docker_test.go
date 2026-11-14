package devbox

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testBoxContract checks the behavior every backend must provide. The box
// must be freshly started and is stopped by the caller.
func testBoxContract(t *testing.T, box Box) {
	t.Helper()
	ctx := context.Background()

	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "README.md"), []byte("# hi\n"), 0o644)
	_ = os.WriteFile(filepath.Join(src, "run.sh"), []byte("#!/bin/sh\necho ran\n"), 0o755)
	_ = os.MkdirAll(filepath.Join(src, ".git"), 0o755)
	_ = os.Symlink("README.md", filepath.Join(src, "LINK.md"))
	if err := box.CopyIn(ctx, src, box.Workdir()); err != nil {
		t.Fatal(err)
	}
	if b, err := box.ReadFile(ctx, "README.md"); err != nil || string(b) != "# hi\n" {
		t.Fatalf("ReadFile = %q, %v", b, err)
	}
	if b, _ := box.ReadFile(ctx, "LINK.md"); string(b) != "# hi\n" {
		t.Errorf("symlink not preserved: %q", b)
	}
	if res, err := box.Exec(ctx, "./run.sh", "", time.Minute, nil); err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "ran") {
		t.Errorf("exec mode not preserved: %+v %v", res, err)
	}
	if err := box.WriteFile(ctx, "sub/dir/new.txt", []byte("binary\x00ok\n")); err != nil {
		t.Fatal(err)
	}
	if b, _ := box.ReadFile(ctx, "/work/project/sub/dir/new.txt"); string(b) != "binary\x00ok\n" {
		t.Errorf("round trip = %q", b)
	}
	ls, err := box.ListDir(ctx, ".")
	if err != nil || !strings.Contains(ls, ".git") || !strings.Contains(ls, "README.md") {
		t.Errorf("ListDir = %q, %v", ls, err)
	}
	res, err := box.Exec(ctx, `pwd && echo "$SECRET_X" && whoami`, "", time.Minute, map[string]string{"SECRET_X": "s3cr3t"})
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Output, "/work/project\ns3cr3t\nroot") {
		t.Errorf("Exec = %+v, %v", res, err)
	}
	if res, _ := box.Exec(ctx, "exit 3", "", time.Minute, nil); res.ExitCode != 3 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
	if res, _ := box.Exec(ctx, "sleep 30", "", 2*time.Second, nil); !res.TimedOut || res.ExitCode != 124 {
		t.Errorf("timeout = %+v", res)
	}
	if _, err := box.ReadFile(ctx, "missing.txt"); err == nil {
		t.Error("ReadFile of missing file should fail")
	}
	// Attach from "another process" sees the same box.
	other, err := Attach(box.Backend(), box.ID(), box.Workdir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := other.ReadFile(ctx, "README.md"); string(b) != "# hi\n" {
		t.Errorf("attached ReadFile = %q", b)
	}
}

func needDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
}

func TestDockerBox(t *testing.T) {
	needDocker(t)
	d := Docker{}
	if d.Ready(context.Background()) != nil {
		t.Skip("devbox image not built")
	}
	box, err := d.Start(context.Background(), "vg-test-"+time.Now().Format("150405.000000"))
	if err != nil {
		t.Fatal(err)
	}
	defer box.Stop(context.Background())
	testBoxContract(t, box)
}

func TestFirecrackerBox(t *testing.T) {
	needDocker(t)
	f := Firecracker{}
	if err := f.Ready(context.Background()); err != nil {
		t.Skip(err)
	}
	t0 := time.Now()
	box, err := f.Start(context.Background(), "vg-test-fc-"+time.Now().Format("150405.000000"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("microVM ready in %s", time.Since(t0).Round(10*time.Millisecond))
	defer box.Stop(context.Background())
	testBoxContract(t, box)

	// What the microVM adds over a container: systemd and a Docker engine.
	ctx := context.Background()
	if res, _ := box.Exec(ctx, "ps -p 1 -o comm=", "", time.Minute, nil); !strings.Contains(res.Output, "systemd") {
		t.Errorf("PID 1 = %q, want systemd", res.Output)
	}
	if res, _ := box.Exec(ctx, "docker info --format '{{.ServerVersion}}'", "", 2*time.Minute, nil); res.ExitCode != 0 {
		t.Errorf("docker engine not usable in the guest: %+v", res)
	}
	// Outbound network: DNS + HTTPS, package installs, and pulling and
	// running a container inside the VM (`docker compose`-style quickstarts).
	if res, _ := box.Exec(ctx, "curl -fsS -o /dev/null -w '%{http_code}' https://pypi.org/simple/", "", time.Minute, nil); res.ExitCode != 0 {
		t.Errorf("guest has no HTTPS access: %+v", res)
	}
	if res, _ := box.Exec(ctx, "pip install -q six && python -c 'import six'", "", 3*time.Minute, nil); res.ExitCode != 0 {
		t.Errorf("pip install failed in the guest: %+v", res)
	}
	if res, _ := box.Exec(ctx, "docker run --rm alpine:3.20 echo from-a-container", "", 3*time.Minute, nil); res.ExitCode != 0 || !strings.Contains(res.Output, "from-a-container") {
		t.Errorf("docker run inside the guest failed: %+v", res)
	}
}

func TestWriteTar(t *testing.T) {
	src := t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, "a", "b"), 0o755)
	_ = os.WriteFile(filepath.Join(src, "a", "b", "f.txt"), []byte("x"), 0o600)
	_ = os.Symlink("a/b/f.txt", filepath.Join(src, "l"))
	var buf bytes.Buffer
	if err := writeTar(&buf, src); err != nil {
		t.Fatal(err)
	}
	got := map[string]byte{}
	tr := tar.NewReader(&buf)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[h.Name] = h.Typeflag
		if h.Name == "a/b/f.txt" && h.Mode&0o777 != 0o600 {
			t.Errorf("mode = %o", h.Mode)
		}
	}
	if got["a/"] != tar.TypeDir || got["a/b/f.txt"] != tar.TypeReg || got["l"] != tar.TypeSymlink {
		t.Errorf("entries = %v", got)
	}
}

func TestResolve(t *testing.T) {
	cases := map[string]string{
		"":              "/work/project",
		"README.md":     "/work/project/README.md",
		"a/../b":        "/work/project/b",
		"/etc/hosts":    "/etc/hosts",
		"/work/../root": "/root",
	}
	for in, want := range cases {
		if got := Resolve("/work/project", in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}
