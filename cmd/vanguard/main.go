// Command vanguard tests quickstarts by having an agent follow them like a
// new developer would.
//
//	vanguard run <repo-url|dir> --doc README.md --goal "…" [--success "cmd"]
//	vanguard devbox build
//	vanguard version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"vanguard.run/internal/devbox"
	"vanguard.run/internal/mcpexec"
	"vanguard.run/internal/pipeline"
	"vanguard.run/internal/runner"
	"vanguard.run/internal/targets"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(ctx, os.Args[2:])
	case "devbox":
		err = cmdDevbox(ctx, os.Args[2:])
	case "devbox-mcp": // internal: launched by agy as the devbox MCP server
		err = cmdDevboxMCP(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("vanguard", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "vanguard: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "vanguard:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `vanguard.run: an agent that tests your quickstart like a new developer

Usage:
  vanguard run <repo-url|dir> [flags]   run the newhire agent on one quickstart
  vanguard devbox build                 build the blank-slate devbox image
  vanguard version

Run 'vanguard run -h' for flags.
`)
}

type agentFlags struct {
	model, effort, agyBin, workRoot, out string
	image, docker, memory, cpus          string
	timeout                              time.Duration
	keep, unsafeHost                     bool
}

func (a *agentFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&a.model, "model", "gemini-3.8-flash-medium", "agy model")
	fs.StringVar(&a.effort, "effort", "", "agy reasoning effort (low|medium|high|xhigh|max)")
	fs.StringVar(&a.agyBin, "agy", "agy", "path to the agy binary")
	fs.DurationVar(&a.timeout, "timeout", 15*time.Minute, "max time for the agent per target")
	fs.StringVar(&a.workRoot, "work", filepath.Join(os.TempDir(), "vanguard"), "root for throwaway workspaces")
	fs.StringVar(&a.out, "out", "runs", "directory for run artifacts")
	fs.BoolVar(&a.keep, "keep", false, "keep the workspace and container after the run")
	fs.StringVar(&a.image, "image", devbox.DefaultImage, "devbox image")
	fs.StringVar(&a.docker, "docker", "docker", "docker binary")
	fs.StringVar(&a.memory, "memory", "4g", "devbox memory limit")
	fs.StringVar(&a.cpus, "cpus", "2", "devbox CPU limit")
	fs.BoolVar(&a.unsafeHost, "unsafe-host", false, "run commands on this machine (agy terminal sandbox only) instead of a devbox container")
}

func (a *agentFlags) config(ctx context.Context) (pipeline.Config, error) {
	cfg := pipeline.Config{
		Runner:   runner.Agy{Bin: a.agyBin, Model: a.model, Effort: a.effort, Timeout: a.timeout, Docker: a.docker},
		WorkRoot: a.workRoot,
		OutDir:   a.out,
		Keep:     a.keep,
		Model:    a.model,
	}
	if a.unsafeHost {
		fmt.Fprintln(os.Stderr, "⚠ --unsafe-host: quickstart commands run on this machine, not in a container")
		return cfg, nil
	}
	dc := devbox.Config{Image: a.image, Docker: a.docker, Memory: a.memory, CPUs: a.cpus, PidsLimit: 1024}
	if !devbox.ImageExists(ctx, dc) {
		return cfg, fmt.Errorf("devbox image %s not found; run 'vanguard devbox build' (or pass --unsafe-host)", a.image)
	}
	cfg.Devbox = &dc
	return cfg, nil
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var af agentFlags
	af.register(fs)
	var t targets.Target
	fs.StringVar(&t.DocPath, "doc", "README.md", "quickstart doc, relative to the repo root")
	fs.StringVar(&t.Goal, "goal", "", "what 'done' means, e.g. \"get a reply from the Gemini API\"")
	fs.StringVar(&t.Success, "success", "", "optional ground-truth shell command (exit 0 = goal reached)")
	fs.StringVar(&t.Ref, "ref", "", "git branch or tag")
	fs.StringVar(&t.Name, "name", "", "target name (default: derived from repo)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: vanguard run <repo-url|dir> [flags]")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("run: exactly one repo URL or directory is required")
	}
	t.Repo = pos[0]

	cfg, err := af.config(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, af.timeout+2*time.Minute)
	defer cancel()

	where := "devbox"
	if cfg.Devbox == nil {
		where = "host"
	}
	fmt.Fprintf(os.Stderr, "▶ newhire → %s (%s) with %s in %s\n", t.Slug(), t.DocPath, af.model, where)
	run, err := pipeline.Execute(ctx, cfg, t)
	if run != nil {
		fmt.Println(run.Markdown())
		fmt.Fprintf(os.Stderr, "artifacts: %s\n", filepath.Join(af.out, run.Target, run.ID+".{json,md,jsonl}"))
	}
	return err
}

// parseInterspersed parses flags that may appear before or after positional
// arguments, which the standard flag package does not allow on its own.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func cmdDevbox(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devbox", flag.ContinueOnError)
	image := fs.String("image", devbox.DefaultImage, "image tag")
	docker := fs.String("docker", "docker", "docker binary")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "build" {
		return errors.New("usage: vanguard devbox build [--image tag]")
	}
	return devbox.Build(ctx, devbox.Config{Image: *image, Docker: *docker}, os.Stderr)
}

func cmdDevboxMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devbox-mcp", flag.ContinueOnError)
	container := fs.String("container", "", "devbox container name")
	workdir := fs.String("workdir", "", "workspace path inside the container")
	logPath := fs.String("log", "", "JSONL exec log")
	docker := fs.String("docker", "docker", "docker binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *container == "" || *workdir == "" {
		return errors.New("devbox-mcp: --container and --workdir are required")
	}
	srv := &mcpexec.Server{Exec: devbox.Attach(*container, *workdir, *docker), Workdir: *workdir, Version: version}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		srv.Log = f
	}
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
