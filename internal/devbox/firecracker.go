package devbox

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

//go:embed vm/Dockerfile vm/entrypoint.sh vm/vanguard-agent.service vm/vanguard-init vm/agent/main.go
var vmFiles embed.FS

// DefaultVMImage is the microVM runner image built by Firecracker.Build.
const DefaultVMImage = "vanguard-vmbox:latest"

// Firecracker is the microVM backend: each box is a container (with
// /dev/kvm) that boots a Firecracker microVM. The guest is the devbox image
// plus systemd and the Docker engine; vanguard talks to an agent inside the
// guest over HTTP via a loopback-only published port. The container only
// supplies networking; the isolation boundary is the VM.
type Firecracker struct {
	Image       string // runner image, default DefaultVMImage
	DevboxImage string // guest base image for Build, default DefaultImage
	VCPUs       int    // default 2
	MemoryMiB   int    // default 4096
	Disk        string // guest disk size, default "16G"
	Bin         string // docker binary, default "docker"
	BootTimeout time.Duration
}

func (f Firecracker) Name() string     { return "firecracker" }
func (f Firecracker) image() string    { return orDefault(f.Image, DefaultVMImage) }
func (f Firecracker) bin() string      { return orDefault(f.Bin, "docker") }
func (f Firecracker) Describe() string { return "firecracker:" + f.image() }

// Ready checks for KVM access and the runner image.
func (f Firecracker) Ready(ctx context.Context) error {
	for _, dev := range []string{"/dev/kvm", "/dev/net/tun"} {
		f, err := os.OpenFile(dev, os.O_RDWR, 0)
		if err != nil {
			return fmt.Errorf("firecracker backend needs read/write access to %s: %w", dev, err)
		}
		f.Close()
	}
	if exec.CommandContext(ctx, f.bin(), "image", "inspect", f.image()).Run() != nil {
		return fmt.Errorf("microVM image %s not found; run 'vanguard devbox build --backend firecracker'", f.image())
	}
	return nil
}

// Build builds the runner image (guest rootfs, kernel, Firecracker). The
// devbox image must already exist; it is the guest's base.
func (f Firecracker) Build(ctx context.Context, out *os.File) error {
	dir, err := os.MkdirTemp("", "vanguard-vmbox-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	err = fs.WalkDir(vmFiles, "vm", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := vmFiles.ReadFile(p)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, strings.TrimPrefix(p, "vm/"))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "agent", "go.mod"), []byte("module vanguard-agent\n\ngo 1.27\n"), 0o644); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, f.bin(), "build", "-t", f.image(),
		"--build-arg", "DEVBOX_IMAGE="+orDefault(f.DevboxImage, DefaultImage), dir)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("devbox: build microVM image: %w", err)
	}
	return nil
}

// Start boots a fresh microVM and waits for its agent.
func (f Firecracker) Start(ctx context.Context, name string) (Box, error) {
	token := randomToken()
	vcpus, mem, disk := f.VCPUs, f.MemoryMiB, orDefault(f.Disk, "16G")
	if vcpus <= 0 {
		vcpus = 2
	}
	if mem <= 0 {
		mem = 4096
	}
	args := []string{"run", "-d",
		"--name", name,
		"--label", "vanguard.run=devbox",
		"--device", "/dev/kvm",
		"--device", "/dev/net/tun",
		"--cap-add", "NET_ADMIN",
		"--sysctl", "net.ipv4.ip_forward=1",
		"-p", "127.0.0.1::7000",
		"-e", "VG_TOKEN=" + token,
		"-e", fmt.Sprintf("VG_VCPUS=%d", vcpus),
		"-e", fmt.Sprintf("VG_MEM_MIB=%d", mem),
		"-e", "VG_DISK=" + disk,
		f.image(),
	}
	out, err := exec.CommandContext(ctx, f.bin(), args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("devbox: start microVM: %v: %s", err, strings.TrimSpace(string(out)))
	}
	b, err := attachVM(ctx, f.bin(), name, DefaultWorkdir)
	if err != nil {
		_ = exec.Command(f.bin(), "rm", "-f", name).Run()
		return nil, err
	}
	timeout := f.BootTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if err := b.waitReady(ctx, timeout); err != nil {
		logs, _ := exec.Command(f.bin(), "logs", "--tail", "40", name).CombinedOutput()
		_ = b.Stop(context.Background())
		return nil, fmt.Errorf("%w\n--- last console output ---\n%s", err, logs)
	}
	return b, nil
}

// vmBox talks to the in-guest agent.
type vmBox struct {
	name, workdir, bin string
	base               string // http://127.0.0.1:PORT
	token              string
	http               *http.Client
}

// attachVM finds a running microVM's agent address and token. The token is
// only in the container's environment, readable by the host user.
func attachVM(ctx context.Context, bin, name, workdir string) (*vmBox, error) {
	out, err := exec.CommandContext(ctx, bin, "inspect", "--format",
		`{{json .Config.Env}}|{{(index (index .NetworkSettings.Ports "7000/tcp") 0).HostPort}}`, name).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("devbox: inspect %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	envJSON, port, ok := strings.Cut(strings.TrimSpace(string(out)), "|")
	if !ok || port == "" {
		return nil, fmt.Errorf("devbox: %s has no published agent port", name)
	}
	var env []string
	if err := json.Unmarshal([]byte(envJSON), &env); err != nil {
		return nil, fmt.Errorf("devbox: inspect env: %w", err)
	}
	var token string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "VG_TOKEN="); ok {
			token = v
		}
	}
	if token == "" {
		return nil, fmt.Errorf("devbox: %s has no agent token", name)
	}
	return &vmBox{name: name, workdir: workdir, bin: bin, base: "http://127.0.0.1:" + port, token: token,
		http: &http.Client{}}, nil
}

func (b *vmBox) Backend() string { return "firecracker" }
func (b *vmBox) ID() string      { return b.name }
func (b *vmBox) Workdir() string { return b.workdir }

func (b *vmBox) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		res, err := b.do(rctx, http.MethodPost, "/exec", nil, jsonBody(map[string]any{"command": "true", "timeout_ms": 2000}))
		cancel()
		if err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("devbox: microVM %s did not become ready within %s", b.name, timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (b *vmBox) do(ctx context.Context, method, p string, q url.Values, body io.Reader) (*http.Response, error) {
	u := b.base + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.http.Do(req)
}

func jsonBody(v any) io.Reader {
	bs, _ := json.Marshal(v)
	return bytes.NewReader(bs)
}

func agentError(res *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return fmt.Errorf("%s", strings.TrimSpace(string(msg)))
}

func (b *vmBox) Exec(ctx context.Context, command, cwd string, timeout time.Duration, env map[string]string) (Result, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancel()
	res, err := b.do(rctx, http.MethodPost, "/exec", nil, jsonBody(map[string]any{
		"command": command, "cwd": Resolve(b.workdir, cwd), "timeout_ms": timeout.Milliseconds(), "env": env,
	}))
	if err != nil {
		return Result{}, fmt.Errorf("devbox: exec: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("devbox: exec: %w", agentError(res))
	}
	var r struct {
		ExitCode int    `json:"exit_code"`
		Output   string `json:"output"`
		TimedOut bool   `json:"timed_out"`
	}
	if err := json.NewDecoder(res.Body).Decode(&r); err != nil {
		return Result{}, fmt.Errorf("devbox: exec: %w", err)
	}
	return Result{ExitCode: r.ExitCode, Output: r.Output, TimedOut: r.TimedOut}, nil
}

func (b *vmBox) ReadFile(ctx context.Context, p string) ([]byte, error) {
	res, err := b.do(ctx, http.MethodGet, "/file", url.Values{"path": {Resolve(b.workdir, p)}}, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, agentError(res)
	}
	return io.ReadAll(res.Body)
}

func (b *vmBox) WriteFile(ctx context.Context, p string, data []byte) error {
	res, err := b.do(ctx, http.MethodPut, "/file", url.Values{"path": {Resolve(b.workdir, p)}}, bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return agentError(res)
	}
	return nil
}

func (b *vmBox) ListDir(ctx context.Context, p string) (string, error) {
	res, err := b.do(ctx, http.MethodGet, "/list", url.Values{"path": {Resolve(b.workdir, p)}}, nil)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", agentError(res)
	}
	out, err := io.ReadAll(res.Body)
	return string(out), err
}

// CopyIn streams hostDir as a tar archive to the agent.
func (b *vmBox) CopyIn(ctx context.Context, hostDir, boxDir string) error {
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(writeTar(pw, hostDir)) }()
	res, err := b.do(ctx, http.MethodPut, "/tar", url.Values{"dir": {Resolve(b.workdir, boxDir)}}, pr)
	if err != nil {
		pr.CloseWithError(err)
		return fmt.Errorf("devbox: copy in: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("devbox: copy in: %w", agentError(res))
	}
	return nil
}

func (b *vmBox) Stop(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, b.bin, "rm", "-f", b.name).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such container") {
		return fmt.Errorf("devbox: remove microVM: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeTar writes dir's contents (regular files, dirs, symlinks) as a tar
// stream with paths relative to dir.
func writeTar(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // skip sockets, devices, fifos
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			h.Name += "/"
		}
		h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(errors.New("devbox: no randomness: " + err.Error()))
	}
	return hex.EncodeToString(b[:])
}
