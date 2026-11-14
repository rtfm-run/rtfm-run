package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtfm.run/internal/ghapp"
)

func cmdGitHubApp(ctx context.Context, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "create":
			return cmdGitHubAppCreate(ctx, args[1:])
		case "webhook-url":
			return cmdGitHubAppWebhookURL(ctx, args[1:])
		}
	}
	return errors.New(`usage:
  rtfm github-app create [--base-url URL] [--webhook-url URL] [--org ORG] [--name NAME]
  rtfm github-app webhook-url <URL> [--env-file FILE]   repoint webhooks (e.g. after a tunnel restart)`)
}

// cmdGitHubAppWebhookURL updates where GitHub sends the App's webhooks.
func cmdGitHubAppWebhookURL(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("github-app webhook-url", flag.ContinueOnError)
	envFile := fs.String("env-file", orDefault(env("RTFM_ENV_FILE"), filepath.Join(defaultConfigDir(), "service.env")), "service configuration")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return errors.New("usage: rtfm github-app webhook-url <URL>  (a tunnel root like https://x.trycloudflare.com gets /webhooks/github appended)")
	}
	hook, err := webhookURL(pos[0])
	if err != nil {
		return err
	}
	if err := loadEnvFile(*envFile); err != nil {
		return err
	}
	e, err := loadServiceEnv(false)
	if err != nil {
		return err
	}
	if err := e.App.SetWebhookURL(ctx, hook); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "webhooks now go to %s\n", hook)
	return nil
}

// webhookURL validates u and, if it has no path, appends /webhooks/github.
func webhookURL(u string) (string, error) {
	p, err := url.Parse(strings.TrimSpace(u))
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" {
		return "", fmt.Errorf("invalid webhook URL %q", u)
	}
	if p.Path == "" || p.Path == "/" {
		p.Path = "/webhooks/github"
	}
	return p.String(), nil
}

func defaultConfigDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "rtfm")
	}
	return "."
}

// cmdGitHubAppCreate registers rtfm's GitHub App with the manifest flow:
// a local page posts the manifest to GitHub, the user confirms there, and
// GitHub redirects back here with a one-time code that we exchange for the
// App's credentials. Secrets go straight to 0600 files and are never printed.
func cmdGitHubAppCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("github-app create", flag.ContinueOnError)
	baseURL := fs.String("base-url", orDefault(env("RTFM_BASE_URL"), "http://localhost:8080"), "URL of 'rtfm serve' as your browser sees it (sign-in callbacks)")
	hookFlag := fs.String("webhook-url", "", "public URL GitHub sends webhooks to, e.g. a tunnel (default <base-url>/webhooks/github)")
	org := fs.String("org", "", "register the App under this organization instead of your account")
	name := fs.String("name", "", "App name (globally unique on GitHub; default rtfm-dev-XXXX)")
	public := fs.Bool("public", false, "let any account install the App (default: only the owner)")
	dir := fs.String("dir", defaultConfigDir(), "where to write github-app.pem and service.env")
	force := fs.Bool("force", false, "overwrite an existing service.env")
	noBrowser := fs.Bool("no-browser", false, "print the URL instead of opening a browser")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	base := strings.TrimRight(*baseURL, "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errors.New("--base-url must be the http(s) URL where 'rtfm serve' will be reachable")
	}
	hook := base + "/webhooks/github"
	if *hookFlag != "" {
		if hook, err = webhookURL(*hookFlag); err != nil {
			return err
		}
	}
	if hu, _ := url.Parse(hook); isLocalHost(hu.Hostname()) {
		fmt.Fprintf(os.Stderr, "warning: %s is not reachable from GitHub, so webhooks (push triggers, installs) won't arrive.\n"+
			"         Start a tunnel (e.g. 'cloudflared tunnel --url http://localhost:8080') and pass --webhook-url,\n"+
			"         or later: rtfm github-app webhook-url <tunnel-url>\n", hook)
	}
	envPath := filepath.Join(*dir, "service.env")
	pemPath := filepath.Join(*dir, "github-app.pem")
	if _, err := os.Stat(envPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (an App is already configured); pass --force to replace it", envPath)
	}
	if *name == "" {
		*name = "rtfm-dev-" + randHex(2)
	}
	state := randHex(16)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	local := "http://" + ln.Addr().String()
	manifest := ghapp.NewManifest(*name, base, hook, local+"/done", *public)
	mjson, err := json.Marshal(manifest)
	if err != nil {
		return err
	}

	type result struct {
		app ghapp.Created
		err error
	}
	done := make(chan result, 1)
	finish := func(r result) {
		select {
		case done <- r:
		default:
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = formPage.Execute(w, map[string]string{"Action": ghapp.ManifestFormURL("", *org, state), "Manifest": string(mjson), "Name": *name})
	})
	mux.HandleFunc("GET /done", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			http.Error(w, "state mismatch; start again with 'rtfm github-app create'", http.StatusBadRequest)
			return
		}
		app, err := ghapp.ConvertManifest(r.Context(), "", nil, q.Get("code"))
		if err == nil {
			err = saveAppConfig(app, base, envPath, pemPath)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			_ = donePage.Execute(w, map[string]any{"Err": err.Error()})
		} else {
			_ = donePage.Execute(w, map[string]any{"App": app, "Install": "https://github.com/apps/" + app.Slug + "/installations/new"})
		}
		finish(result{app, err})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	fmt.Fprintf(os.Stderr, "Registering GitHub App %q (sign-in %s, webhooks %s)\n", *name, base, hook)
	fmt.Fprintf(os.Stderr, "Open %s and click \"Create GitHub App\" on GitHub.\n", local)
	if !*noBrowser {
		_ = exec.Command("xdg-open", local).Start()
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	var r result
	select {
	case <-ctx.Done():
		return fmt.Errorf("no response from GitHub: %w", ctx.Err())
	case r = <-done:
	}
	time.Sleep(300 * time.Millisecond) // let the success page finish sending
	if r.err != nil {
		return r.err
	}
	fmt.Fprintf(os.Stderr, "\nCreated %s (id %d), owned by %s.\n", r.app.HTMLURL, r.app.ID, r.app.Owner.Login)
	fmt.Fprintf(os.Stderr, "  private key  %s\n  config       %s\n", pemPath, envPath)
	fmt.Fprintf(os.Stderr, "\nNext:\n  1. Install it on repos: https://github.com/apps/%s/installations/new\n", r.app.Slug)
	fmt.Fprintf(os.Stderr, "  2. rtfm serve --env-file %s\n", envPath)
	return nil
}

// saveAppConfig writes the private key and a service.env with everything
// 'rtfm serve' needs (both 0600). An existing RTFM_SECRET_KEY is
// kept, so credentials already encrypted with it stay readable.
func saveAppConfig(app ghapp.Created, base, envPath, pemPath string) error {
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		return err
	}
	if err := writePrivate(pemPath, []byte(app.PEM)); err != nil {
		return err
	}
	vals := map[string]string{
		"RTFM_BASE_URL":               base,
		"GITHUB_APP_ID":               fmt.Sprint(app.ID),
		"GITHUB_APP_SLUG":             app.Slug,
		"GITHUB_APP_CLIENT_ID":        app.ClientID,
		"GITHUB_APP_CLIENT_SECRET":    app.ClientSecret,
		"GITHUB_APP_WEBHOOK_SECRET":   app.WebhookSecret,
		"GITHUB_APP_PRIVATE_KEY_FILE": pemPath,
	}
	old, _ := readEnvFile(envPath)
	for _, k := range []string{"RTFM_SECRET_KEY", "RTFM_DATABASE_URL", "RTFM_DASHBOARD_URL"} {
		switch {
		case old[k] != "":
			vals[k] = old[k]
		case env(k) != "":
			vals[k] = env(k)
		}
	}
	if vals["RTFM_SECRET_KEY"] == "" {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		vals["RTFM_SECRET_KEY"] = base64.StdEncoding.EncodeToString(key)
	}
	if vals["GITHUB_APP_WEBHOOK_SECRET"] == "" {
		return errors.New("GitHub returned no webhook secret; set one in the App settings and add GITHUB_APP_WEBHOOK_SECRET to " + envPath)
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# rtfm service configuration, written by 'rtfm github-app create'.\n# Contains secrets: keep it private.\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, vals[k])
	}
	if vals["RTFM_DATABASE_URL"] == "" {
		b.WriteString("# RTFM_DATABASE_URL=postgres://user:pass@host:5432/rtfm\n")
	}
	return writePrivate(envPath, []byte(b.String()))
}

// writePrivate writes data to path with 0600 permissions, atomically.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readEnvFile parses KEY=VALUE lines (blank lines and # comments skipped,
// optional surrounding quotes removed).
func readEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, sc.Err()
}

// loadEnvFile applies an env file without overriding variables that are
// already set, so the real environment always wins.
func loadEnvFile(path string) error {
	if path == "" {
		return nil
	}
	vals, err := readEnvFile(path)
	if err != nil {
		return fmt.Errorf("--env-file: %w", err)
	}
	for k, v := range vals {
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return nil
}

func isLocalHost(h string) bool {
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified())
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var formPage = template.Must(template.New("form").Parse(`<!doctype html>
<meta charset="utf-8"><title>Register {{.Name}}</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1>Registering {{.Name}}</h1>
<p>Sending you to GitHub to confirm the new App…</p>
<form id="f" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById("f").submit()</script>
</body>`))

var donePage = template.Must(template.New("done").Parse(`<!doctype html>
<meta charset="utf-8"><title>rtfm GitHub App</title>
<body style="font-family:system-ui;max-width:40rem;margin:4rem auto;padding:0 1rem">
{{if .Err}}<h1>Something went wrong</h1><p>{{.Err}}</p><p>Check the terminal, then run <code>rtfm github-app create</code> again.</p>
{{else}}<h1>Created {{.App.Name}}</h1>
<p>Credentials are saved. You can close this tab.</p>
<p>Next, <a href="{{.Install}}">install it on the repos</a> whose quickstarts you want tested.</p>{{end}}
</body>`))
