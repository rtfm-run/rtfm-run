package creds

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakePrompter struct {
	value string
	save  bool
	calls int
}

func (f *fakePrompter) Prompt(context.Context, string, Request) (string, bool, bool, error) {
	f.calls++
	return f.value, f.save, f.value != "", nil
}

func TestStore(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "vg", "credentials.json")}
	if _, ok, _ := s.Get("X_KEY"); ok {
		t.Fatal("empty store returned a value")
	}
	if err := s.Set("bad name", "v"); err == nil {
		t.Error("invalid name accepted")
	}
	if err := s.Set("X_KEY", "secret-1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.Get("X_KEY"); !ok || v != "secret-1" {
		t.Errorf("Get = %q %v", v, ok)
	}
	fi, _ := os.Stat(s.Path)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("store mode = %v, want 0600", fi.Mode().Perm())
	}
	names, _ := s.Names()
	if len(names) != 1 || names[0] != "X_KEY" {
		t.Errorf("Names = %v", names)
	}
	_ = s.Delete("X_KEY")
	if _, ok, _ := s.Get("X_KEY"); ok {
		t.Error("Delete did not remove")
	}
}

func TestBrokerOverSocket(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := &Store{Path: filepath.Join(dir, "c.json")}
	_ = store.Set("SAVED_KEY", "from-store-123")
	fp := &fakePrompter{value: "typed-secret-456", save: true}
	b := &Broker{Store: store, Prompter: fp}

	sess, err := b.Listen(ctx, "demo", filepath.Join(dir, "b.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	c := Client{Path: sess.Path()}

	a, err := c.Request(ctx, Request{Name: "SAVED_KEY", Description: "d"})
	if err != nil || a.Status != StatusProvided || a.Value != "from-store-123" || a.Source != "store" {
		t.Errorf("saved: %+v %v", a, err)
	}
	a, _ = c.Request(ctx, Request{Name: "NEW_KEY", Description: "d", URL: "https://example.com/keys"})
	if a.Status != StatusProvided || a.Value != "typed-secret-456" || a.Source != "prompt" {
		t.Errorf("prompted: %+v", a)
	}
	if v, ok, _ := store.Get("NEW_KEY"); !ok || v != "typed-secret-456" {
		t.Error("prompted value with save=true was not stored")
	}
	a, _ = c.Request(ctx, Request{Name: "lower-case"})
	if a.Status != StatusInvalid {
		t.Errorf("invalid name: %+v", a)
	}

	recs := sess.Records()
	if len(recs) != 3 || recs[1].Name != "NEW_KEY" || recs[1].Status != StatusProvided || recs[1].URL == "" {
		t.Errorf("records = %+v", recs)
	}
	vals := sess.Values()
	if vals["NEW_KEY"] != "typed-secret-456" || len(vals) != 2 {
		t.Errorf("values = %v", vals)
	}
}

func TestBrokerNoPrompter(t *testing.T) {
	b := &Broker{}
	if a := b.Resolve(context.Background(), "t", Request{Name: "API_KEY"}); a.Status != StatusUnavailable || a.Value != "" {
		t.Errorf("got %+v", a)
	}
	fp := &fakePrompter{} // user skips
	b = &Broker{Prompter: fp}
	if a := b.Resolve(context.Background(), "t", Request{Name: "API_KEY"}); a.Status != StatusUnavailable || fp.calls != 1 {
		t.Errorf("skip: %+v calls=%d", a, fp.calls)
	}
}

func TestRedact(t *testing.T) {
	vals := map[string]string{"API_KEY": "sk-abcdef123456", "SHORT": "ab"}
	in := "API_KEY=sk-abcdef123456\nauth: Bearer sk-abcdef123456 ab"
	out := Redact(in, vals)
	if strings.Contains(out, "sk-abcdef123456") || strings.Count(out, "[REDACTED:API_KEY]") != 2 {
		t.Errorf("Redact = %q", out)
	}
	if !strings.HasSuffix(out, " ab") {
		t.Errorf("short value should not be redacted: %q", out)
	}
	p := filepath.Join(t.TempDir(), "log.jsonl")
	_ = os.WriteFile(p, []byte(in), 0o644)
	if err := RedactFile(p, vals); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "sk-abcdef123456") {
		t.Error("RedactFile left the secret")
	}
}
