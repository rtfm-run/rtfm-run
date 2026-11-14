// Command rtfm tests quickstarts by having an agent follow them like a
// new developer would.
//
//	rtfm run <repo-url|dir> --doc README.md --goal "…" [--success "cmd"]
//	rtfm sweep targets.json [--parallel N]
//	rtfm pr <target|run.json> [--open]
//	rtfm creds set|list|rm [NAME]
//	rtfm devbox build
//	rtfm version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rtfm.run/internal/creds"
	"rtfm.run/internal/devbox"
	"rtfm.run/internal/diagnose"
	"rtfm.run/internal/github"
	"rtfm.run/internal/guidelines"
	"rtfm.run/internal/mcpexec"
	"rtfm.run/internal/pipeline"
	"rtfm.run/internal/prs"
	"rtfm.run/internal/report"
	"rtfm.run/internal/runner"
	"rtfm.run/internal/stats"
	"rtfm.run/internal/targets"
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
	case "sweep":
		err = cmdSweep(ctx, os.Args[2:])
	case "serve":
		err = cmdServe(ctx, os.Args[2:])
	case "worker":
		err = cmdWorker(ctx, os.Args[2:])
	case "migrate":
		err = cmdMigrate(ctx, os.Args[2:])
	case "pr-draft":
		err = cmdPRDraft(ctx, os.Args[2:])
	case "github-app":
		err = cmdGitHubApp(ctx, os.Args[2:])
	case "pr":
		err = cmdPR(ctx, os.Args[2:])
	case "creds":
		err = cmdCreds(os.Args[2:])
	case "devbox":
		err = cmdDevbox(ctx, os.Args[2:])
	case "devbox-mcp": // internal: launched by agy as the devbox MCP server
		err = cmdDevboxMCP(ctx, os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("rtfm", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "rtfm: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rtfm:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `rtfm.run: an agent that tests your quickstart like a new developer

Usage:
  rtfm run <repo-url|dir> [flags]   run the newhire agent on one quickstart
  rtfm sweep <targets.json> [flags]  run many quickstarts and summarize
  rtfm pr <target|run.json> [flags]  preview (or --open) a pull request for a verified fix
  rtfm creds set|list|rm [NAME]     manage credentials quickstarts may need
  rtfm devbox build                 build the blank-slate devbox image
  rtfm serve [--workers N]          run the API (+ workers) for the dashboard
  rtfm worker [--workers N]         run workers only (KVM hosts)
  rtfm migrate                      apply database migrations
  rtfm pr-draft <run-id>                prepare a finished run's pull request draft
  rtfm github-app create            register the GitHub App (one click on GitHub)
  rtfm version

Run 'rtfm run -h' or 'rtfm sweep -h' for flags.
`)
}

type agentFlags struct {
	model, effort, agyBin, workRoot, out string
	image, docker, memory, cpus, backend string
	timeout                              time.Duration
	keep, unsafeHost, fix, verify        bool
	noPrompt                             bool
}

func (a *agentFlags) register(fs *flag.FlagSet, fixByDefault bool) {
	fs.BoolVar(&a.fix, "fix", fixByDefault, "when the quickstart has issues, propose a doc fix")
	fs.BoolVar(&a.verify, "verify", true, "rerun a fresh newcomer on the fixed doc to prove the fix (with --fix)")
	fs.StringVar(&a.model, "model", "gemini-3.8-flash-medium", "agy model")
	fs.StringVar(&a.effort, "effort", "", "agy reasoning effort (low|medium|high|xhigh|max)")
	fs.StringVar(&a.agyBin, "agy", "agy", "path to the agy binary")
	fs.DurationVar(&a.timeout, "timeout", 15*time.Minute, "max time for the agent per target")
	fs.StringVar(&a.workRoot, "work", filepath.Join(os.TempDir(), "rtfm"), "root for throwaway workspaces")
	fs.StringVar(&a.out, "out", "runs", "directory for run artifacts")
	fs.BoolVar(&a.keep, "keep", false, "keep the workspace and container after the run")
	fs.StringVar(&a.backend, "backend", "auto", "devbox backend: firecracker (microVM with systemd and Docker inside), docker (container), or auto (firecracker when available, else docker)")
	fs.StringVar(&a.image, "image", "", "devbox image (default: the backend's built image)")
	fs.StringVar(&a.docker, "docker", "docker", "docker binary")
	fs.StringVar(&a.memory, "memory", "4g", "devbox memory limit")
	fs.StringVar(&a.cpus, "cpus", "2", "devbox CPU limit")
	fs.BoolVar(&a.noPrompt, "no-prompt", false, "never ask for credentials; use saved ones only (runs that need others are reported as blocked)")
	fs.BoolVar(&a.unsafeHost, "unsafe-host", false, "run commands on this machine (agy terminal sandbox only) instead of a devbox container")
}

func (a *agentFlags) config(ctx context.Context) (pipeline.Config, error) {
	cfg := pipeline.Config{
		Runner:   runner.Agy{Bin: a.agyBin, Model: a.model, Effort: a.effort, Timeout: a.timeout, Docker: a.docker},
		WorkRoot: a.workRoot,
		OutDir:   a.out,
		Keep:     a.keep,
		Model:    a.model,
		Fix:      a.fix,
		Verify:   a.fix && a.verify,
	}
	cfg.GitHubToken, _ = githubToken() // optional: enables private GitHub repos
	if a.unsafeHost {
		fmt.Fprintln(os.Stderr, "⚠ --unsafe-host: quickstart commands run on this machine, not in a container")
		return cfg, nil
	}
	backend, err := a.newBackend(ctx)
	if err != nil {
		return cfg, err
	}
	if err := backend.Ready(ctx); err != nil {
		return cfg, fmt.Errorf("%w (or pass --unsafe-host)", err)
	}
	cfg.Backend = backend

	store, err := creds.DefaultStore()
	if err != nil {
		return cfg, err
	}
	cfg.Broker = &creds.Broker{Store: store}
	if !a.noPrompt && creds.TTYAvailable() {
		cfg.Broker.Prompter = creds.TTYPrompter{}
	}
	return cfg, nil
}

// budget is the total time for one target: newhire, plus fixer and verify
// runs when fixing.
func (a *agentFlags) budget() time.Duration {
	stages := 1
	if a.fix {
		stages = 2
		if a.verify {
			stages = 3
		}
	}
	return time.Duration(stages)*a.timeout + 3*time.Minute
}

func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var af agentFlags
	af.register(fs, true)
	var t targets.Target
	fs.StringVar(&t.DocPath, "doc", "README.md", "quickstart doc, relative to the repo root")
	fs.StringVar(&t.Goal, "goal", "", "what 'done' means, e.g. \"get a reply from the Gemini API\"")
	fs.StringVar(&t.Success, "success", "", "optional ground-truth shell command (exit 0 = goal reached)")
	fs.StringVar(&t.Ref, "ref", "", "git branch or tag")
	fs.StringVar(&t.Name, "name", "", "target name (default: derived from repo)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: rtfm run <repo-url|dir> [flags]")
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
	ctx, cancel := context.WithTimeout(ctx, af.budget())
	defer cancel()

	where := "host"
	if cfg.Backend != nil {
		where = cfg.Backend.Describe()
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

func (a *agentFlags) newBackend(ctx context.Context) (devbox.Backend, error) {
	if a.backend == "auto" {
		a.backend = "docker"
		fc, err := a.firecracker()
		if err == nil && fc.Ready(ctx) == nil {
			return fc, nil
		}
		fmt.Fprintln(os.Stderr, "ℹ microVM backend unavailable (needs /dev/kvm and 'rtfm devbox build --backend firecracker'); using docker containers")
	}
	switch a.backend {
	case "docker":
		return devbox.Docker{Image: a.image, Bin: a.docker, Memory: a.memory, CPUs: a.cpus, PidsLimit: 1024}, nil
	case "firecracker":
		return a.firecracker()
	default:
		return nil, fmt.Errorf("unknown --backend %q (auto, firecracker or docker)", a.backend)
	}
}

func (a *agentFlags) firecracker() (devbox.Firecracker, error) {
	mib, err := parseMiB(a.memory)
	if err != nil {
		return devbox.Firecracker{}, err
	}
	vcpus := 2
	if _, err := fmt.Sscan(a.cpus, &vcpus); err != nil || vcpus < 1 {
		return devbox.Firecracker{}, fmt.Errorf("--cpus must be a whole number of vCPUs for firecracker, got %q", a.cpus)
	}
	return devbox.Firecracker{Image: a.image, Bin: a.docker, VCPUs: vcpus, MemoryMiB: mib}, nil
}

// parseMiB parses sizes like "4g", "4096m" or "4096" (MiB).
func parseMiB(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1024, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		s = strings.TrimSuffix(s, "m")
	}
	var n int
	if _, err := fmt.Sscan(s, &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory size %q", s)
	}
	return n * mult, nil
}

func cmdDevbox(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devbox", flag.ContinueOnError)
	backend := fs.String("backend", "docker", "docker or firecracker (firecracker also builds the docker devbox it is based on)")
	docker := fs.String("docker", "docker", "docker binary")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "build" {
		return errors.New("usage: rtfm devbox build [--backend docker|firecracker]")
	}
	base := devbox.Docker{Bin: *docker}
	switch *backend {
	case "docker":
		return base.Build(ctx, os.Stderr)
	case "firecracker":
		fmt.Fprintln(os.Stderr, "▶ building the devbox base image")
		if err := base.Build(ctx, os.Stderr); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "▶ building the microVM image (guest rootfs, kernel, Firecracker)")
		return devbox.Firecracker{Bin: *docker}.Build(ctx, os.Stderr)
	default:
		return fmt.Errorf("unknown --backend %q", *backend)
	}
}

func cmdDevboxMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("devbox-mcp", flag.ContinueOnError)
	backend := fs.String("backend", "docker", "devbox backend")
	boxID := fs.String("box", "", "devbox id (container name for docker)")
	workdir := fs.String("workdir", devbox.DefaultWorkdir, "project directory inside the box")
	logPath := fs.String("log", "", "JSONL exec log")
	docker := fs.String("docker", "docker", "docker binary")
	broker := fs.String("broker", "", "credential broker socket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *boxID == "" {
		return errors.New("devbox-mcp: --box is required")
	}
	box, err := devbox.Attach(*backend, *boxID, *workdir, *docker)
	if err != nil {
		return err
	}
	srv := &mcpexec.Server{Box: box, Version: version}
	if *broker != "" {
		srv.Creds = creds.Client{Path: *broker}
	}
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

func cmdSweep(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sweep", flag.ContinueOnError)
	var af agentFlags
	af.register(fs, false)
	parallel := fs.Int("parallel", 2, "targets to run at once")
	only := fs.String("only", "", "comma-separated target names to run (default: all)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: rtfm sweep <targets.json> [flags]")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("sweep: exactly one targets file is required")
	}
	ts, err := targets.Load(pos[0])
	if err != nil {
		return err
	}
	if *only != "" {
		want := map[string]bool{}
		for _, n := range strings.Split(*only, ",") {
			want[targets.Sanitize(n)] = true
		}
		ts = slicesFilter(ts, func(t targets.Target) bool { return want[t.Slug()] })
	}
	if len(ts) == 0 {
		return errors.New("sweep: no targets to run")
	}
	cfg, err := af.config(ctx)
	if err != nil {
		return err
	}
	if *parallel < 1 {
		*parallel = 1
	}

	started := time.Now().UTC()
	fmt.Fprintf(os.Stderr, "▶ sweep: %d targets, %d at a time, %s\n", len(ts), *parallel, af.model)
	results := make([]*report.Run, len(ts))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
		sem  = make(chan struct{}, *parallel)
	)
	for i, t := range ts {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			tctx, cancel := context.WithTimeout(ctx, af.budget())
			defer cancel()
			run, err := pipeline.Execute(tctx, cfg, t)
			if run == nil {
				run = &report.Run{Target: t.Slug(), Repo: t.Repo, DocPath: t.DocPath, Started: time.Now().UTC(), Ended: time.Now().UTC()}
				if err != nil {
					run.Error = err.Error()
				}
			}
			results[i] = run
			mu.Lock()
			defer mu.Unlock()
			done++
			fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", done, len(ts), progressLine(run))
		})
	}
	wg.Wait()

	sum := stats.Summarize(results)
	md := stats.Markdown(sum, results)
	dir := filepath.Join(af.out, "sweeps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stamp := started.Format("20060102-150405")
	type ref struct{ Target, ID string }
	var refs []ref
	for _, r := range results {
		if r != nil {
			refs = append(refs, ref{r.Target, r.ID})
		}
	}
	b, err := json.MarshalIndent(map[string]any{"started": started, "summary": sum, "runs": refs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, stamp+".json"), b, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, stamp+".md"), []byte(md), 0o644); err != nil {
		return err
	}
	fmt.Println(md)
	fmt.Fprintf(os.Stderr, "sweep report: %s\n", filepath.Join(dir, stamp+".{md,json}"))
	return ctx.Err()
}

func progressLine(r *report.Run) string {
	pass, fail, unsup := r.Counts()
	switch {
	case r.Error != "":
		return fmt.Sprintf("⚠️  %s: %s", r.Target, r.Error)
	case r.Blocked():
		return fmt.Sprintf("🔑 %s: blocked, needs %s (rtfm creds set NAME)", r.Target, strings.Join(r.MissingCredentials(), ", "))
	case r.Works() && fail+unsup == 0:
		return fmt.Sprintf("✅ %s (%d steps, %s)", r.Target, pass, r.Duration().Round(time.Second))
	}
	line := fmt.Sprintf("❌ %s: %d failed, %d unsupported (%s)", r.Target, fail, unsup, r.Duration().Round(time.Second))
	if r.Works() {
		line = fmt.Sprintf("🟡 %s works, but %d unsupported/%d failed steps (%s)", r.Target, unsup, fail, r.Duration().Round(time.Second))
	}
	if r.Fix != nil {
		line += fmt.Sprintf(" → fix %s (%d→%d issues)", r.Fix.Status, r.Fix.IssuesBefore, r.Fix.IssuesAfter)
	}
	return line
}

func slicesFilter[T any](in []T, keep func(T) bool) []T {
	var out []T
	for _, v := range in {
		if keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func cmdCreds(args []string) error {
	usage := errors.New("usage: rtfm creds set NAME | list | rm NAME")
	if len(args) == 0 {
		return usage
	}
	store, err := creds.DefaultStore()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list", "ls":
		names, err := store.Names()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintf(os.Stderr, "no saved credentials (%s)\n", store.Path)
		}
		for _, n := range names {
			fmt.Println(n)
		}
		return nil
	case "set":
		if len(args) != 2 {
			return usage
		}
		name := args[1]
		if !creds.ValidName(name) {
			return fmt.Errorf("invalid name %q: use an environment variable name like GEMINI_API_KEY", name)
		}
		if !creds.TTYAvailable() {
			return errors.New("creds set needs a terminal to read the value")
		}
		v, _, ok, err := creds.TTYPrompter{}.Prompt(context.Background(), "your quickstarts", creds.Request{Name: name, Description: "Saved for future rtfm runs."})
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no value entered")
		}
		if err := store.Set(name, v); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "saved %s to %s\n", name, store.Path)
		return nil
	case "rm", "delete":
		if len(args) != 2 {
			return usage
		}
		return store.Delete(args[1])
	default:
		return usage
	}
}

func cmdPR(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	out := fs.String("out", "runs", "directory with run artifacts")
	open := fs.Bool("open", false, "actually fork/branch, commit and open the pull request (default: dry run)")
	allowPartial := fs.Bool("allow-partial", false, "also propose fixes that only improved the quickstart")
	signoff := fs.Bool("signoff", false, "add a DCO Signed-off-by line with your git identity (only if you agree to the project's DCO)")
	claSigned := fs.Bool("cla-signed", false, "confirm you have signed the project's CLA")
	attest := fs.Bool("attest", false, "confirm the template's personal checklist items are true for you, and tick them")
	issue := fs.String("issue", "", "issue URL to reference (for projects that require an issue first)")
	model := fs.String("model", "gemini-3.8-flash-medium", "agy model used to apply the contribution guidelines")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: rtfm pr <target-name|path/to/run.json> [--open] [--signoff] [--attest] [--cla-signed] [--issue URL] [--allow-partial]")
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("pr: exactly one target name or run file is required")
	}
	runPath := pos[0]
	if !strings.HasSuffix(runPath, ".json") {
		runPath = filepath.Join(*out, targets.Sanitize(runPath), "latest.json")
	}
	run, err := loadRun(runPath)
	if err != nil {
		return err
	}
	if err := prs.Eligible(run, *allowPartial); err != nil {
		return err
	}
	diagnose.Apply(run) // refresh with current rules (runs may predate fixes)
	dir := filepath.Dir(runPath)
	ext := filepath.Ext(run.DocPath)
	fixed, err := os.ReadFile(filepath.Join(dir, run.ID+".fixed"+ext))
	if err != nil {
		return fmt.Errorf("fixed doc: %w", err)
	}
	original, err := originalDoc(ctx, dir, run, fixed)
	if err != nil {
		return err
	}
	var verify *report.Run
	if run.Fix.VerifyRunID != "" {
		verify, _ = loadRun(filepath.Join(dir, run.Fix.VerifyRunID+".json"))
	}

	p := prs.Build(run, verify, original, fixed)
	repo, isGitHub := github.ParseRepo(run.Repo)
	token, tokenErr := githubToken()
	gh := &github.Client{Token: token} // a token is optional for reading public repos

	// Follow the repository's contribution guidelines.
	var outcome guidelines.Outcome
	if isGitHub {
		fmt.Fprintf(os.Stderr, "▶ reading %s's contribution guidelines\n", repo)
		found, err := guidelines.Discover(ctx, gh, repo, "")
		if err != nil {
			return fmt.Errorf("read contribution guidelines: %w", err)
		}
		var decision *guidelines.Decision
		if !found.Empty() {
			// Reuse the dry run's decision so --open posts exactly what was reviewed.
			cache, key := filepath.Join(dir, run.ID+".guidelines.json"), guidelines.Key(found, p, *model)
			if decision = guidelines.Load(cache, key); decision != nil {
				fmt.Fprintf(os.Stderr, "▶ using the reviewed interpretation of the guidelines (%s)\n", cache)
			} else {
				fmt.Fprintf(os.Stderr, "▶ applying them to the pull request\n")
				if decision, err = guidelines.Interpret(ctx, runner.Agy{Model: *model}, repo.String(), found, p); err != nil {
					return err
				}
				if err := guidelines.Save(cache, key, decision); err != nil {
					return err
				}
				if *open {
					return errors.New("the guidelines were interpreted just now, so this text hasn't been reviewed: check it above, then run the same command again to open it")
				}
			}
		}
		opt := guidelines.Options{Signoff: *signoff, CLASigned: *claSigned, Attest: *attest, Issue: *issue}
		if *signoff {
			opt.Identity = gitIdentity(ctx)
		}
		outcome = guidelines.Apply(p, decision, found, opt)
	}

	prFile := filepath.Join(dir, run.ID+".pr.md")
	if err := os.WriteFile(prFile, []byte("# "+p.Title+"\n\n"+p.Body), 0o644); err != nil {
		return err
	}
	base := p.BaseBranch
	if base == "" {
		base = "(default branch)"
	}
	fmt.Printf("Repository: %s\nTested commit: %s\nTarget: %s ← %s\nTitle: %s\n\nCommit message:\n%s\n%s\n```diff\n%s```\n\n",
		run.Repo, orNone(run.BaseCommit), base, p.Branch, p.Title, indent(p.CommitMessage), p.Body, p.Patch)
	if isGitHub {
		fmt.Println(outcome.Render())
	}
	fmt.Fprintf(os.Stderr, "PR text: %s · patch: %s\n", prFile, filepath.Join(dir, run.ID+".patch"))

	switch {
	case !isGitHub:
		fmt.Fprintln(os.Stderr, "Not a GitHub repository: apply the patch with `git apply` and use the PR text above.")
		return nil
	case !*open && !outcome.Ready():
		fmt.Fprintln(os.Stderr, "Dry run: nothing was created. Resolve the items marked ✗ above (⛔ cannot be overridden) before --open.")
		return nil
	case !*open:
		fmt.Fprintln(os.Stderr, "Dry run: nothing was created. Re-run with --open (and the same flags) to fork if needed, push a branch and open this pull request.")
		return nil
	case run.Fix.PRURL != "":
		return fmt.Errorf("a pull request was already opened for this run: %s", run.Fix.PRURL)
	case !outcome.Ready():
		return errors.New("not opening: the repository's contribution guidelines have unresolved items (see above)")
	case tokenErr != nil:
		return tokenErr
	}

	if login, err := gh.User(ctx); err == nil {
		fmt.Fprintf(os.Stderr, "▶ opening the pull request as %s\n", login)
	}
	opened, err := prs.Open(ctx, gh, p)
	if err != nil {
		return err
	}
	if opened.Fork != "" {
		fmt.Fprintf(os.Stderr, "  via fork %s, branch %s\n", opened.Fork, opened.Branch)
	}
	fmt.Println("Opened:", opened.URL)
	run.Fix.PRURL = opened.URL
	return saveRun(run, runPath, filepath.Join(dir, run.ID+".json"))
}

func loadRun(path string) (*report.Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r report.Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// saveRun writes run to each distinct path.
func saveRun(run *report.Run, paths ...string) error {
	b, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, path := range paths {
		abs, _ := filepath.Abs(path)
		if seen[abs] {
			continue
		}
		seen[abs] = true
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// originalDoc returns the doc as tested: saved by the run, or (for runs
// made before originals were saved) reconstructed by reversing the patch.
func originalDoc(ctx context.Context, dir string, run *report.Run, fixed []byte) ([]byte, error) {
	ext := filepath.Ext(run.DocPath)
	if b, err := os.ReadFile(filepath.Join(dir, run.ID+".orig"+ext)); err == nil {
		return b, nil
	}
	tmp, err := os.MkdirTemp("", "rtfm-pr-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	in, outPath := filepath.Join(tmp, "fixed"), filepath.Join(tmp, "orig")
	if err := os.WriteFile(in, fixed, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "patch", "-R", "-s", "-o", outPath, in, filepath.Join(dir, run.ID+".patch"))
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("reconstruct the original doc: %v: %s", err, strings.TrimSpace(string(b)))
	}
	return os.ReadFile(outPath)
}

// githubToken finds a token: GITHUB_TOKEN, GH_TOKEN, or the credential store.
func githubToken() (string, error) {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v, nil
		}
	}
	if store, err := creds.DefaultStore(); err == nil {
		if v, ok, _ := store.Get("GITHUB_TOKEN"); ok {
			return v, nil
		}
	}
	return "", errors.New("no GitHub token: set GITHUB_TOKEN (or GH_TOKEN), or run 'rtfm creds set GITHUB_TOKEN'; it needs permission to fork, push and open pull requests (public_repo scope, or fine-grained contents + pull requests)")
}

// gitIdentity reads the user's git name and email for a DCO sign-off.
func gitIdentity(ctx context.Context) *github.Identity {
	get := func(k string) string {
		out, err := exec.CommandContext(ctx, "git", "config", "--get", k).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return &github.Identity{Name: get("user.name"), Email: get("user.email")}
}

func indent(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ") + "\n"
}

func orNone(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
