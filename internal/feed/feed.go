// Package feed defines the signed content bundle and its verification.
//
// Trust model:
//   - The signature covers the RAW bundle bytes and is verified BEFORE any JSON
//     parse, so a hostile CDN cannot parser-bomb us pre-verification.
//   - A hard byte cap is enforced on the input before anything else.
//   - Ed25519 public key is pinned in the client; the private key never touches
//     a server. Verification is offline and total: fail-closed on any error.
package feed

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MaxBundleBytes caps a downloaded/loaded bundle. 64 KiB is ample for a day of
// one-line items and stops memory-exhaustion before parsing.
const MaxBundleBytes = 64 * 1024

// Line is one ticker item. The ticker renderer uses only Text/Source/Sponsored;
// URL and Author exist for the `brief` command and web mirror (著作權法 §64
// attribution) and MUST never be emitted into the statusLine ticker.
type Line struct {
	Text      string `json:"t"`                // ≤1 self-written sentence
	Source    string `json:"src"`              // site/author short label (plain text)
	Lang      string `json:"lang"`             // "en" | "zh-TW"
	Topic     string `json:"topic,omitempty"`  // for paid topic feeds
	URL       string `json:"url,omitempty"`    // full link — brief/mirror only, never the ticker
	Author    string `json:"author,omitempty"` // attribution — brief/mirror only
	Sponsored bool   `json:"sponsored,omitempty"`
	// Review is an editorial HOLD marker set by curate on lines that need a human
	// look (unverified number, sensitive/platform topic, low model confidence).
	// It MUST be empty to sign (cmd/sign refuses otherwise); the renderer/brief
	// ignore it. Emptying it = the human editor's explicit "I checked this" act.
	Review string `json:"review,omitempty"`
}

// Bundle is the JSON payload. Version is monotonic (replay defense); Expires is
// short-lived (stale-closed). Sponsored lines get their "Ad:" label from the
// RENDERER, never from feed text.
type Bundle struct {
	Version int       `json:"v"`
	Issued  time.Time `json:"issued"`
	Expires time.Time `json:"expires"`
	Lines   []Line    `json:"lines"`
}

// Envelope is what the CDN serves: raw bundle bytes + a detached signature. The
// signature is over Payload verbatim, so signed bytes == parsed bytes exactly
// (no JSON-canonicalisation ambiguity).
type Envelope struct {
	Payload   json.RawMessage `json:"payload"`   // raw bundle bytes, signed verbatim
	Signature string          `json:"sig"`       // base64(ed25519 signature of Payload)
	KeyID     string          `json:"kid"`       // which pinned key signed it
}

var (
	ErrTooLarge   = errors.New("feed: bundle exceeds size cap")
	ErrBadSig     = errors.New("feed: signature verification failed")
	ErrUnknownKey = errors.New("feed: unknown key id")
	ErrExpired    = errors.New("feed: bundle expired")
	ErrRollback   = errors.New("feed: version older than last seen (replay/rollback)")
)

// Verify parses the envelope, checks the size cap, verifies the detached
// signature over the raw payload bytes BEFORE parsing the bundle, then parses
// and applies freshness + monotonic-version checks. Any failure returns an
// error and a nil bundle — callers MUST render nothing on error (fail-closed).
//
// pinnedKeys maps key id -> public key. lastSeenVersion is the persisted
// high-water mark (rollback defense); pass -1 if none yet. now is injected for
// testability.
func Verify(raw []byte, pinnedKeys map[string]ed25519.PublicKey, lastSeenVersion int, now time.Time) (*Bundle, error) {
	if len(raw) > MaxBundleBytes {
		return nil, ErrTooLarge
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("feed: bad envelope: %w", err)
	}
	pub, ok := pinnedKeys[env.KeyID]
	if !ok {
		return nil, ErrUnknownKey
	}
	sig, err := base64.StdEncoding.DecodeString(env.Signature)
	if err != nil {
		return nil, fmt.Errorf("feed: bad signature encoding: %w", err)
	}
	// Verify BEFORE parsing the payload.
	if !ed25519.Verify(pub, env.Payload, sig) {
		return nil, ErrBadSig
	}
	if len(env.Payload) > MaxBundleBytes {
		return nil, ErrTooLarge
	}

	// Only now do we parse the (authenticated, size-capped) payload.
	dec := json.NewDecoder(bytes.NewReader(env.Payload))
	dec.DisallowUnknownFields()
	var b Bundle
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("feed: bad payload: %w", err)
	}
	if now.After(b.Expires) {
		return nil, ErrExpired
	}
	if b.Version <= lastSeenVersion {
		return nil, ErrRollback
	}
	return &b, nil
}
