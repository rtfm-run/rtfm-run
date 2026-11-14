package ghapp

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testApp(t *testing.T, srv *httptest.Server) *App {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ID: 42, Slug: "vanguard-run", ClientID: "Iv1.abc", ClientSecret: "shh", WebhookSecret: "hook", Key: key}
	if srv != nil {
		a.APIBase, a.WebBase = srv.URL, srv.URL
	}
	return a
}

func TestParseKey(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	for _, p := range [][]byte{pkcs1, pkcs8} {
		if k, err := ParseKey(p); err != nil || !k.Equal(key) {
			t.Errorf("ParseKey: %v", err)
		}
	}
	if _, err := ParseKey([]byte("nope")); err == nil {
		t.Error("garbage accepted")
	}
}

func TestJWT(t *testing.T) {
	a := testApp(t, nil)
	now := time.Unix(1_800_000_000, 0)
	jwt, err := a.JWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", jwt)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&a.Key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Errorf("signature: %v", err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	json.Unmarshal(claims, &c)
	if c["iss"] != "42" || int64(c["iat"].(float64)) != now.Unix()-60 || int64(c["exp"].(float64)) != now.Add(9*time.Minute).Unix() {
		t.Errorf("claims = %v", c)
	}
}

func TestInstallationTokenIsCached(t *testing.T) {
	var calls atomic.Int32
	var app *App
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations/7/access_tokens" && r.Method == "POST":
			calls.Add(1)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
				t.Errorf("app JWT not sent: %q", r.Header.Get("Authorization"))
			}
			json.NewEncoder(w).Encode(map[string]any{"token": "ghs_inst", "expires_at": time.Now().Add(time.Hour)})
		case r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer ghs_inst" {
				t.Errorf("installation token not used: %q", r.Header.Get("Authorization"))
			}
			json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []map[string]any{{"id": 5, "full_name": "o/r", "default_branch": "main"}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	app = testApp(t, srv)
	for i := 0; i < 3; i++ {
		tok, err := app.InstallationToken(context.Background(), 7)
		if err != nil || tok != "ghs_inst" {
			t.Fatalf("token = %q %v", tok, err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("token requested %d times, want 1 (cached)", calls.Load())
	}
	repos, err := app.InstallationRepos(context.Background(), 7)
	if err != nil || len(repos) != 1 || repos[0].FullName != "o/r" {
		t.Errorf("repos = %+v %v", repos, err)
	}
}

func TestExchangeCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path != "/login/oauth/access_token" || r.Form.Get("client_secret") != "shh" {
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("code") == "bad" {
			json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "ghu_x", "refresh_token": "ghr_y", "expires_in": 28800})
	}))
	defer srv.Close()
	a := testApp(t, srv)
	tok, err := a.ExchangeCode(context.Background(), "good", "https://app/cb")
	if err != nil || tok.AccessToken != "ghu_x" || tok.RefreshToken != "ghr_y" || tok.ExpiresAt == nil {
		t.Errorf("token = %+v %v", tok, err)
	}
	if _, err := a.ExchangeCode(context.Background(), "bad", "https://app/cb"); err == nil || !strings.Contains(err.Error(), "bad_verification_code") {
		t.Errorf("bad code: %v", err)
	}
	if u := a.AuthorizeURL("st8", "https://app/cb"); !strings.Contains(u, "client_id=Iv1.abc") || !strings.Contains(u, "state=st8") {
		t.Errorf("authorize url = %s", u)
	}
}

func TestVerifySignature(t *testing.T) {
	a := testApp(t, nil)
	body := []byte(`{"action":"created"}`)
	mac := hmac.New(sha256.New, []byte("hook"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !a.VerifySignature(body, good) {
		t.Error("valid signature rejected")
	}
	if a.VerifySignature([]byte(`{"action":"deleted"}`), good) || a.VerifySignature(body, "sha256=00") || a.VerifySignature(body, "") {
		t.Error("invalid signature accepted")
	}
}
