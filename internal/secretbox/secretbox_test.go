package secretbox

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	b, err := New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := b.Seal([]byte("ghu_token"))
	s2, _ := b.Seal([]byte("ghu_token"))
	if bytes.Equal(s1, s2) || bytes.Contains(s1, []byte("ghu_token")) {
		t.Error("ciphertexts must differ and not contain the plaintext")
	}
	if p, err := b.Open(s1); err != nil || string(p) != "ghu_token" {
		t.Errorf("open = %q %v", p, err)
	}
	s1[len(s1)-1] ^= 1
	if _, err := b.Open(s1); err == nil {
		t.Error("tampered ciphertext opened")
	}
	if _, err := New("c2hvcnQ="); err == nil {
		t.Error("short key accepted")
	}
}
