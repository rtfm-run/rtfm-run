package ghapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewManifest(t *testing.T) {
	m := NewManifest("vanguard-dev-ab12", "https://x.example.com/", "", "http://127.0.0.1:9/done", false)
	if m.HookAttributes.URL != "https://x.example.com/webhooks/github" || !m.HookAttributes.Active {
		t.Errorf("hook = %+v", m.HookAttributes)
	}
	if len(m.CallbackURLs) != 1 || m.CallbackURLs[0] != "https://x.example.com/auth/github/callback" {
		t.Errorf("callbacks = %v", m.CallbackURLs)
	}
	if m.DefaultPermissions["contents"] != "write" || m.DefaultPermissions["pull_requests"] != "write" || m.Public {
		t.Errorf("manifest = %+v", m)
	}
	if m2 := NewManifest("n", "http://localhost:8080", "https://t.example/webhooks/github", "r", false); m2.HookAttributes.URL != "https://t.example/webhooks/github" || m2.CallbackURLs[0] != "http://localhost:8080/auth/github/callback" {
		t.Errorf("split urls: %+v", m2)
	}
	b, _ := json.Marshal(m)
	for _, k := range []string{`"hook_attributes"`, `"redirect_url"`, `"default_permissions"`, `"default_events":["push"]`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("json missing %s: %s", k, b)
		}
	}
}

func TestManifestFormURL(t *testing.T) {
	if got := ManifestFormURL("", "", "s1"); got != "https://github.com/settings/apps/new?state=s1" {
		t.Error(got)
	}
	if got := ManifestFormURL("", "my-org", "s 2"); got != "https://github.com/organizations/my-org/settings/apps/new?state=s+2" {
		t.Error(got)
	}
}

func TestConvertManifest(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/code123/conversions" {
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, 404)
			return
		}
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "conversion must be unauthenticated", 400)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"id": 7, "slug": "vanguard-dev-ab12", "client_id": "Iv23.x", "client_secret": "cs",
			"webhook_secret": "ws", "pem": pemKey, "html_url": "https://github.com/apps/vanguard-dev-ab12", "owner": map[string]any{"login": "me", "id": 1}})
	}))
	defer srv.Close()
	got, err := ConvertManifest(context.Background(), srv.URL, nil, "code123")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 || got.Slug != "vanguard-dev-ab12" || got.WebhookSecret != "ws" || got.Owner.Login != "me" {
		t.Errorf("got %+v", got)
	}
	if _, err := ConvertManifest(context.Background(), srv.URL, nil, "../../user"); err == nil {
		t.Error("path-like code accepted")
	}
	if _, err := ConvertManifest(context.Background(), srv.URL, nil, "other"); err == nil {
		t.Error("expected error for unknown code")
	}
}

func TestSetWebhookURL(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/app/hook/config" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			http.Error(w, "unexpected", 400)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := testApp(t, srv).SetWebhookURL(context.Background(), "https://new.example/webhooks/github"); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "https://new.example/webhooks/github" {
		t.Errorf("got %v", got)
	}
}
