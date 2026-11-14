package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"vanguard.run/internal/db"
	"vanguard.run/internal/devbox"
	"vanguard.run/internal/ghapp"
	"vanguard.run/internal/runner"
	"vanguard.run/internal/secretbox"
	"vanguard.run/internal/server"
	"vanguard.run/internal/worker"
)

// serviceEnv is the service configuration, from the environment.
type serviceEnv struct {
	DatabaseURL  string
	SecretKey    string
	BaseURL      string
	DashboardURL string
	App          *ghapp.App
}

func env(k string) string { return strings.TrimSpace(os.Getenv(k)) }

// loadServiceEnv reads configuration. full=false (workers) needs only the
// database, the secret key and the App's id and private key.
func loadServiceEnv(full bool) (*serviceEnv, error) {
	e := &serviceEnv{
		DatabaseURL:  env("VANGUARD_DATABASE_URL"),
		SecretKey:    env("VANGUARD_SECRET_KEY"),
		BaseURL:      strings.TrimRight(env("VANGUARD_BASE_URL"), "/"),
		DashboardURL: env("VANGUARD_DASHBOARD_URL"),
	}
	var missing []string
	need := func(name, v string) {
		if v == "" {
			missing = append(missing, name)
		}
	}
	need("VANGUARD_DATABASE_URL", e.DatabaseURL)
	need("VANGUARD_SECRET_KEY", e.SecretKey)
	app := &ghapp.App{
		Slug:          env("GITHUB_APP_SLUG"),
		ClientID:      env("GITHUB_APP_CLIENT_ID"),
		ClientSecret:  env("GITHUB_APP_CLIENT_SECRET"),
		WebhookSecret: env("GITHUB_APP_WEBHOOK_SECRET"),
	}
	if id := env("GITHUB_APP_ID"); id != "" {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_ID must be a number")
		}
		app.ID = n
	} else {
		missing = append(missing, "GITHUB_APP_ID")
	}
	pemBytes := []byte(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
	if f := env("GITHUB_APP_PRIVATE_KEY_FILE"); len(pemBytes) == 0 && f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
		}
		pemBytes = b
	}
	if len(pemBytes) == 0 {
		missing = append(missing, "GITHUB_APP_PRIVATE_KEY (or _FILE)")
	} else {
		k, err := ghapp.ParseKey(pemBytes)
		if err != nil {
			return nil, err
		}
		app.Key = k
	}
	if full {
		need("VANGUARD_BASE_URL", e.BaseURL)
		need("GITHUB_APP_SLUG", app.Slug)
		need("GITHUB_APP_CLIENT_ID", app.ClientID)
		need("GITHUB_APP_CLIENT_SECRET", app.ClientSecret)
		need("GITHUB_APP_WEBHOOK_SECRET", app.WebhookSecret)
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing configuration: %s", strings.Join(missing, ", "))
	}
	e.App = app
	return e, nil
}

type workerFlags struct {
	workers       int
	work, out     string
	model, agyBin string
	timeout       time.Duration
	memory, cpus  string
	docker        string
}

func (wf *workerFlags) register(fs *flag.FlagSet, defaultWorkers int) {
	fs.IntVar(&wf.workers, "workers", defaultWorkers, "runs to execute at once on this host (0 = API only)")
	fs.StringVar(&wf.work, "work", filepath.Join(os.TempDir(), "vanguard"), "root for throwaway workspaces")
	fs.StringVar(&wf.out, "out", "service-runs", "directory for run artifacts (logs, patches)")
	fs.StringVar(&wf.model, "model", "gemini-3.8-flash-medium", "default agent model")
	fs.StringVar(&wf.agyBin, "agy", "agy", "path to the agy binary")
	fs.DurationVar(&wf.timeout, "timeout", 15*time.Minute, "max time per agent stage")
	fs.StringVar(&wf.memory, "memory", "4g", "devbox memory")
	fs.StringVar(&wf.cpus, "cpus", "2", "devbox CPUs")
	fs.StringVar(&wf.docker, "docker", "docker", "docker binary")
}

func (wf *workerFlags) config(d *db.DB, e *serviceEnv, box *secretbox.Box, log *slog.Logger) worker.Config {
	host, _ := os.Hostname()
	return worker.Config{
		DB: d, App: e.App, Secrets: box, Log: log,
		Name:        fmt.Sprintf("%s-%d", host, os.Getpid()),
		Concurrency: wf.workers,
		WorkRoot:    wf.work, OutDir: wf.out,
		DefaultModel: wf.model, Timeout: wf.timeout,
		NewBackend: func(ctx context.Context, name string) (devbox.Backend, error) {
			af := agentFlags{backend: name, docker: wf.docker, memory: wf.memory, cpus: wf.cpus}
			b, err := af.newBackend(ctx)
			if err != nil {
				return nil, err
			}
			return b, b.Ready(ctx)
		},
		NewRunner: func(model string) runner.Runner {
			return runner.Agy{Bin: wf.agyBin, Model: model, Timeout: wf.timeout, Docker: wf.docker}
		},
	}
}

func openService(ctx context.Context, full bool) (*serviceEnv, *db.DB, *secretbox.Box, error) {
	e, err := loadServiceEnv(full)
	if err != nil {
		return nil, nil, nil, err
	}
	box, err := secretbox.New(e.SecretKey)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("VANGUARD_SECRET_KEY: %w", err)
	}
	d, err := db.Open(ctx, e.DatabaseURL)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := d.Migrate(ctx); err != nil {
		d.Close()
		return nil, nil, nil, err
	}
	return e, d, box, nil
}

// cmdServe runs the API (and, by default, workers) until interrupted.
func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":"+orDefault(env("PORT"), "8080"), "listen address")
	var wf workerFlags
	wf.register(fs, 1)
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	e, d, box, err := openService(ctx, true)
	if err != nil {
		return err
	}
	defer d.Close()

	srv := &server.Server{DB: d, App: e.App, Secrets: box, BaseURL: e.BaseURL, DashboardURL: e.DashboardURL, Log: log}
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 2)
	go func() {
		log.Info("api listening", "addr", *addr, "base_url", e.BaseURL)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	if wf.workers > 0 {
		go func() {
			log.Info("workers started", "count", wf.workers)
			if err := worker.Run(ctx, wf.config(d, e, box, log)); err != nil && !errors.Is(err, context.Canceled) {
				errc <- err
			}
		}()
	}
	go purgeLoop(ctx, d, log)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return hs.Shutdown(sctx)
}

// cmdWorker runs workers only (for KVM hosts separate from the API).
func cmdWorker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	var wf workerFlags
	wf.register(fs, 2)
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	if wf.workers < 1 {
		return errors.New("worker: --workers must be at least 1")
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	e, d, box, err := openService(ctx, false)
	if err != nil {
		return err
	}
	defer d.Close()
	log.Info("workers started", "count", wf.workers)
	err = worker.Run(ctx, wf.config(d, e, box, log))
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func cmdMigrate(ctx context.Context) error {
	url := env("VANGUARD_DATABASE_URL")
	if url == "" {
		return errors.New("set VANGUARD_DATABASE_URL")
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "migrations applied")
	return nil
}

func purgeLoop(ctx context.Context, d *db.DB, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.PurgeExpired(ctx); err != nil {
				log.Warn("purge failed", "err", err)
			}
		}
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
