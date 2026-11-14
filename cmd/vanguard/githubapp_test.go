package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vanguard.run/internal/ghapp"
)

func TestSaveAppConfig(t *testing.T) {
	dir := t.TempDir()
	envPath, pemPath := filepath.Join(dir, "service.env"), filepath.Join(dir, "github-app.pem")
	t.Setenv("VANGUARD_SECRET_KEY", "")
	t.Setenv("VANGUARD_DATABASE_URL", "postgres://u:p@h/db")
	app := ghapp.Created{ID: 7, Slug: "vg", ClientID: "Iv23.x", ClientSecret: "cs", WebhookSecret: "ws", PEM: "PEMDATA"}
	if err := saveAppConfig(app, "https://x.example.com", envPath, pemPath); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{envPath, pemPath} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	vals, err := readEnvFile(envPath)
	if err != nil {
		t.Fatal(err)
	}
	if vals["GITHUB_APP_ID"] != "7" || vals["GITHUB_APP_WEBHOOK_SECRET"] != "ws" || vals["GITHUB_APP_PRIVATE_KEY_FILE"] != pemPath ||
		vals["VANGUARD_DATABASE_URL"] != "postgres://u:p@h/db" || len(vals["VANGUARD_SECRET_KEY"]) != 44 {
		t.Errorf("vals = %v", vals)
	}
	// Re-registering keeps the secret key, so stored credentials stay readable.
	key := vals["VANGUARD_SECRET_KEY"]
	app.ID = 8
	if err := saveAppConfig(app, "https://x.example.com", envPath, pemPath); err != nil {
		t.Fatal(err)
	}
	vals, _ = readEnvFile(envPath)
	if vals["VANGUARD_SECRET_KEY"] != key || vals["GITHUB_APP_ID"] != "8" {
		t.Errorf("after re-register: %v", vals)
	}
}

func TestLoadEnvFileEnvironmentWins(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.env")
	os.WriteFile(p, []byte("# c\nVG_A=from-file\nexport VG_B=\"quoted\"\n\nVG_C='single'\n"), 0o600)
	t.Setenv("VG_A", "from-env")
	os.Unsetenv("VG_B")
	os.Unsetenv("VG_C")
	t.Cleanup(func() { os.Unsetenv("VG_B"); os.Unsetenv("VG_C") })
	if err := loadEnvFile(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("VG_A") != "from-env" || os.Getenv("VG_B") != "quoted" || os.Getenv("VG_C") != "single" {
		t.Errorf("A=%q B=%q C=%q", os.Getenv("VG_A"), os.Getenv("VG_B"), os.Getenv("VG_C"))
	}
	os.WriteFile(p, []byte("not a pair\n"), 0o600)
	if err := loadEnvFile(p); err == nil || !strings.Contains(err.Error(), ":1:") {
		t.Errorf("err = %v", err)
	}
}

func TestFormPageEscapesManifest(t *testing.T) {
	var b strings.Builder
	formPage.Execute(&b, map[string]string{"Action": "https://github.com/settings/apps/new?state=s", "Manifest": `{"name":"a\"><script>x</script>"}`, "Name": "a"})
	if strings.Contains(b.String(), "<script>x") {
		t.Error("manifest not escaped")
	}
}

func TestIsLocalHost(t *testing.T) {
	for h, want := range map[string]bool{"localhost": true, "127.0.0.1": true, "192.168.1.4": true, "abc.trycloudflare.com": false, "1.1.1.1": false} {
		if isLocalHost(h) != want {
			t.Errorf("%s: want %v", h, want)
		}
	}
}

func TestWebhookURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://a.trycloudflare.com":   "https://a.trycloudflare.com/webhooks/github",
		"https://a.trycloudflare.com/":  "https://a.trycloudflare.com/webhooks/github",
		"https://a.example/custom/hook": "https://a.example/custom/hook",
	} {
		if got, err := webhookURL(in); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if _, err := webhookURL("ftp://x"); err == nil {
		t.Error("ftp accepted")
	}
}
