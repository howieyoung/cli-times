package feed

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func mkEnvelope(t *testing.T, priv ed25519.PrivateKey, kid string, b Bundle) []byte {
	t.Helper()
	payload, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payload)
	env := Envelope{Payload: payload, Signature: base64.StdEncoding.EncodeToString(sig), KeyID: kid}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func goodBundle(now time.Time) Bundle {
	return Bundle{
		Version: 42,
		Issued:  now.Add(-time.Hour),
		Expires: now.Add(24 * time.Hour),
		Lines:   []Line{{Text: "OpenAI 發布新模型", Source: "hn", Lang: "zh-TW"}},
	}
}

func TestVerifyHappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	raw := mkEnvelope(t, priv, "k1", goodBundle(now))
	b, err := Verify(raw, keys, -1, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Version != 42 || len(b.Lines) != 1 {
		t.Fatalf("bad bundle: %+v", b)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	raw := mkEnvelope(t, priv, "k1", goodBundle(now))

	// Flip a byte inside the signed payload region.
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	env.Payload = json.RawMessage(strings.Replace(string(env.Payload), "OpenAI", "EVILco", 1))
	tampered, _ := json.Marshal(env)

	if _, err := Verify(tampered, keys, -1, now); err != ErrBadSig {
		t.Fatalf("expected ErrBadSig, got %v", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	otherPub, _, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": otherPub} // pinned key != signer
	raw := mkEnvelope(t, priv, "k1", goodBundle(now))
	if _, err := Verify(raw, keys, -1, now); err != ErrBadSig {
		t.Fatalf("expected ErrBadSig, got %v", err)
	}
}

func TestVerifyRejectsUnknownKeyID(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	raw := mkEnvelope(t, priv, "k-unknown", goodBundle(now))
	if _, err := Verify(raw, keys, -1, now); err != ErrUnknownKey {
		t.Fatalf("expected ErrUnknownKey, got %v", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	b := goodBundle(now)
	b.Expires = now.Add(-time.Minute) // already expired
	raw := mkEnvelope(t, priv, "k1", b)
	if _, err := Verify(raw, keys, -1, now); err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
}

func TestVerifyRejectsRollback(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	raw := mkEnvelope(t, priv, "k1", goodBundle(now)) // version 42
	// Already saw version 42 (or newer) → replaying 42 must be refused.
	if _, err := Verify(raw, keys, 42, now); err != ErrRollback {
		t.Fatalf("expected ErrRollback, got %v", err)
	}
	// A newer version passes.
	b := goodBundle(now)
	b.Version = 43
	raw = mkEnvelope(t, priv, "k1", b)
	if _, err := Verify(raw, keys, 42, now); err != nil {
		t.Fatalf("version 43 over high-water 42 should pass, got %v", err)
	}
}

func TestVerifyRejectsOversize(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	keys := map[string]ed25519.PublicKey{"k1": pub}
	big := make([]byte, MaxBundleBytes+1)
	if _, err := Verify(big, keys, -1, time.Now()); err != ErrTooLarge {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}
