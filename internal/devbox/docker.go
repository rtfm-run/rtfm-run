package devbox

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

//go:embed Dockerfile
var dockerfile []byte

// DefaultImage is the devbox image tag built by Build.
const DefaultImage = "vanguard-devbox:latest"

// Docker is the container backend. With rootless Docker, root in the box
// maps to the invoking user on the host. Nothing from the host is mounted
// into the box.
type Docker struct {
	Image     string // default DefaultImage
	Memory    string // e.g. "4g"
	CPUs      string // e.g. "2"
	PidsLimit int    // e.g. 1024
	Bin       string // docker binary, default "docker"
}

func (d Docker) Name() string     { return "docker" }
func (d Docker) image() string    { return orDefault(d.Image, DefaultImage) }
func (d Docker) bin() string      { return orDefault(d.Bin, "docker") }
func (d Docker) Describe() string { return "docker:" + d.image() }

// Build builds the devbox image from the embedded Dockerfile.
func (d Docker) Build(ctx context.Context, out *os.File) error {
	cmd := exec.CommandContext(ctx, d.bin(), "build", "-t", d.image(), "-")
	cmd.Stdin = bytes.NewReader(dockerfile)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devbox: docker build: %w", err)
	}
	return nil
}

// Ready checks that the image exists locally.
func (d Docker) Ready(ctx context.Context) error {
	if exec.CommandContext(ctx, d.bin(), "image", "inspect", d.image()).Run() != nil {
		return fmt.Errorf("devbox image %s not found; run 'vanguard devbox build'", d.image())
	}
	return nil
}

// Start launches a fresh container with normal outbound network access
// (quickstarts download things) and resource limits.
func (d Docker) Start(ctx context.Context, name string) (Box, error) {
	startMu.Lock()
	defer startMu.Unlock()
	perBox := parseMemMiB(d.Memory)
	if perBox == 0 {
		perBox = 2048 // no limit set: assume a typical box
	}
	if err := checkCapacity(ctx, d.bin(), perBox); err != nil {
		return nil, err
	}
	args := []string{"run", "-d",
		"--name", name,
		"--hostname", "devbox",
		"--label", "vanguard.run=devbox",
		"--security-opt", "no-new-privileges",
		"-w", DefaultWorkdir,
	}
	if d.Memory != "" {
		args = append(args, "--memory", d.Memory)
	}
	if d.CPUs != "" {
		args = append(args, "--cpus", d.CPUs)
	}
	if d.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprint(d.PidsLimit))
	}
	args = append(args, d.image(), "sleep", "infinity")
	out, err := exec.CommandContext(ctx, d.bin(), args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("devbox: docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	b := &dockerBox{name: name, workdir: DefaultWorkdir, bin: d.bin()}
	if _, _, code, err := b.raw(ctx, nil, "mkdir", "-p", DefaultWorkdir); err != nil || code != 0 {
		_ = b.Stop(context.Background())
		return nil, fmt.Errorf("devbox: prepare workdir: exit %d: %v", code, err)
	}
	return b, nil
}

type dockerBox struct {
	name, workdir, bin string
}

func (b *dockerBox) Backend() string { return "docker" }
func (b *dockerBox) ID() string      { return b.name }
func (b *dockerBox) Workdir() string { return b.workdir }

// raw runs `docker exec [-i] <box> args...` with optional stdin and returns
// stdout, stderr and the exit code. err is set only if docker itself failed.
func (b *dockerBox) raw(ctx context.Context, stdin []byte, args ...string) ([]byte, []byte, int, error) {
	full := []string{"exec"}
	if stdin != nil {
		full = append(full, "-i")
	}
	full = append(full, b.name)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, b.bin, full...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return stdout.Bytes(), stderr.Bytes(), ee.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, -1, fmt.Errorf("devbox: docker exec: %w", err)
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// Exec passes env values through docker's own process environment
// ("-e NAME" without a value), never on the command line, so they don't show
// up in host process listings.
func (b *dockerBox) Exec(ctx context.Context, command, cwd string, timeout time.Duration, env map[string]string) (Result, error) {
	cwd = Resolve(b.workdir, cwd)
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec", "-w", cwd}
	var cmdEnv []string
	for k, v := range env {
		args = append(args, "-e", k)
		cmdEnv = append(cmdEnv, k+"="+v)
	}
	args = append(args, b.name, "bash", "-lc", command)
	cmd := exec.CommandContext(ctx, b.bin, args...)
	if len(cmdEnv) > 0 {
		cmd.Env = append(os.Environ(), cmdEnv...)
	}
	cmd.WaitDelay = 5 * time.Second
	var buf tailBuffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	res := Result{Output: buf.String()}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.ExitCode, res.TimedOut = 124, true
		return res, nil
	case err == nil:
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	return res, fmt.Errorf("devbox: docker exec: %w", err)
}

func (b *dockerBox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	p = Resolve(b.workdir, p)
	out, errOut, code, err := b.raw(ctx, nil, "cat", "--", p)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("read %s: %s", p, strings.TrimSpace(string(errOut)))
	}
	return out, nil
}

func (b *dockerBox) WriteFile(ctx context.Context, p string, data []byte) error {
	p = Resolve(b.workdir, p)
	_, errOut, code, err := b.raw(ctx, data, "sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", p)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("write %s: %s", p, strings.TrimSpace(string(errOut)))
	}
	return nil
}

func (b *dockerBox) ListDir(ctx context.Context, p string) (string, error) {
	p = Resolve(b.workdir, p)
	out, errOut, code, err := b.raw(ctx, nil, "ls", "-la", "--group-directories-first", "--", p)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("list %s: %s", p, strings.TrimSpace(string(errOut)))
	}
	return string(out), nil
}

// CopyIn copies hostDir's contents (including dotfiles like .git) into boxDir.
func (b *dockerBox) CopyIn(ctx context.Context, hostDir, boxDir string) error {
	boxDir = Resolve(b.workdir, boxDir)
	if _, _, code, err := b.raw(ctx, nil, "mkdir", "-p", path.Dir(boxDir)); err != nil || code != 0 {
		return fmt.Errorf("devbox: copy in: mkdir exit %d: %v", code, err)
	}
	// "src/." copies the directory's contents, creating boxDir if needed.
	out, err := exec.CommandContext(ctx, b.bin, "cp", strings.TrimRight(hostDir, "/")+"/.", b.name+":"+boxDir).CombinedOutput()
	if err != nil {
		return fmt.Errorf("devbox: docker cp: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *dockerBox) Stop(ctx context.Context) error {
	if b == nil {
		return nil
	}
	out, err := exec.CommandContext(ctx, b.bin, "rm", "-f", b.name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("devbox: docker rm: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
