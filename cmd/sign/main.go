// Command sign is the OFFLINE signing tool. The private key it creates must
// never touch a server or CI. Two subcommands:
//
//	sign keygen -out keys/            # create ed25519 keypair; print pinned pubkey line
//	sign bundle -key keys/priv.key -kid k1 -in bundle.json -out envelope.json
//
// The signature is over the raw bundle bytes, so signed bytes == displayed bytes.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/howieyoung/cli-times/internal/feed"
	"github.com/howieyoung/cli-times/internal/sanitize"
	"github.com/rivo/uniseg"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: sign <keygen|bundle> ...")
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "bundle":
		bundle(os.Args[2:])
	default:
		fail("unknown subcommand %q", os.Args[1])
	}
}

func keygen(args []string) {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "keys", "directory to write keypair")
	kid := fs.String("kid", "k1", "key id")
	fs.Parse(args)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	must(os.MkdirAll(*out, 0o700))
	privPath := filepath.Join(*out, "priv.key")
	must(os.WriteFile(privPath, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600))
	pubPath := filepath.Join(*out, "pub.key")
	pubB64 := base64.StdEncoding.EncodeToString(pub)
	must(os.WriteFile(pubPath, []byte(pubB64), 0o644))

	fmt.Fprintf(os.Stderr, "private key (0600) → %s   KEEP OFFLINE\n", privPath)
	fmt.Fprintf(os.Stderr, "public key        → %s\n\n", pubPath)
	fmt.Fprintf(os.Stderr, "pin this in the renderer build:\n")
	fmt.Printf("CLI_TIMES_PUBKEY=%s CLI_TIMES_KID=%s\n", pubB64, *kid)
}

func bundle(args []string) {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	keyPath := fs.String("key", "keys/priv.key", "private key file")
	kid := fs.String("kid", "k1", "key id")
	in := fs.String("in", "", "bundle JSON to sign")
	out := fs.String("out", "", "output envelope path (default stdout)")
	expiresHours := fs.Int("expires-hours", 48, "short replay window: set issued=now, expires=now+N hours")
	stripReview := fs.Bool("strip-review", false, "AUTO publisher: DROP lines still flagged for review (log them) instead of failing")
	minLines := fs.Int("min-lines", 0, "refuse to sign if fewer than N lines survive (never publish a thin/empty feed)")
	fs.Parse(args)
	if *in == "" {
		fail("bundle: -in required")
	}

	privB64, err := os.ReadFile(*keyPath)
	must(err)
	privRaw, err := base64.StdEncoding.DecodeString(string(privB64))
	must(err)
	priv := ed25519.PrivateKey(privRaw)

	payload, err := os.ReadFile(*in)
	must(err)
	// Validate it parses as a Bundle before signing (don't sign garbage), and
	// reject unknown fields so an operator typo can't be silently dropped.
	var b feed.Bundle
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	must(dec.Decode(&b))

	// SANITIZE GATE: refuse to sign if any line would be rejected at render time.
	// This fails loud here instead of the renderer silently skipping a bad line,
	// and it means the signature never blesses content the renderer can't show.
	var kept []feed.Line
	for i, ln := range b.Lines {
		// EDITORIAL HOLD: a line still carrying a review marker has not been signed
		// off. In the human flow (default) we REFUSE the whole bundle. In the AUTO
		// publisher (-strip-review) we DROP that line and keep going, so unreviewed
		// risky content never ships but a clean feed still publishes unattended.
		if strings.TrimSpace(ln.Review) != "" {
			if *stripReview {
				fmt.Fprintf(os.Stderr, "sign: dropping flagged line %d (%s): %q\n", i+1, ln.Review, ln.Text)
				continue
			}
			fail("bundle: line %d is flagged for review (%s)\n       → check it against %s, then remove its \"review\" field (or delete the line) and re-run", i+1, ln.Review, ln.URL)
		}
		if _, r, ok := sanitize.Clean(ln.Text, 0); !ok {
			fail("bundle: line %d rejected by sanitizer (%s): %q", i+1, r, ln.Text)
		} else if uniseg.StringWidth(mustClean(ln.Text)) > 85 {
			fail("bundle: line %d headline exceeds 85 display cols: %q", i+1, ln.Text)
		}
		if _, _, ok := sanitize.Clean(ln.Source, 0); ln.Source != "" && !ok {
			fail("bundle: line %d source rejected by sanitizer: %q", i+1, ln.Source)
		}
		if ln.URL != "" && !urlSafe(ln.URL) {
			fail("bundle: line %d has an unsafe URL: %q", i+1, ln.URL)
		}
		kept = append(kept, ln)
	}
	// Never publish a thin/empty feed (e.g. a bad crawl day dropped everything).
	if len(kept) < *minLines {
		fail("bundle: only %d line(s) survived (min-lines=%d) — refusing to publish a thin feed", len(kept), *minLines)
	}
	b.Lines = kept

	// Short replay window: stamp a fresh issued/expires at sign time regardless
	// of the draft's dates, so a stale draft can't ship a week-long window.
	now := time.Now().UTC()
	b.Issued = now
	b.Expires = now.Add(time.Duration(*expiresHours) * time.Hour)

	// Stamp a MONOTONIC version at sign time (overriding the draft's v, exactly as
	// we override issued/expires). The updater keeps a high-water mark and rejects
	// v <= lastSeen as rollback; hand-made bundles are all v=1, so without this a
	// re-signed bundle would be refused by any machine that already polled, and it
	// would blank at expiry. Unix seconds strictly increase across a human publish
	// cadence, so every publish — curated or hand-written — supersedes the last.
	b.Version = int(now.Unix())

	// Re-marshal to canonical, compact bytes and sign THOSE exact bytes.
	canonical, err := json.Marshal(b)
	must(err)

	sig := ed25519.Sign(priv, canonical)
	env := feed.Envelope{
		Payload:   canonical,
		Signature: base64.StdEncoding.EncodeToString(sig),
		KeyID:     *kid,
	}
	envBytes, err := json.Marshal(env)
	must(err)
	if len(envBytes) > feed.MaxBundleBytes {
		fail("bundle: signed envelope %d bytes exceeds cap %d", len(envBytes), feed.MaxBundleBytes)
	}

	if *out == "" {
		os.Stdout.Write(envBytes)
		fmt.Println()
	} else {
		must(os.WriteFile(*out, envBytes, 0o644))
		fmt.Fprintf(os.Stderr, "signed envelope (v%d, %d lines) → %s\n", b.Version, len(b.Lines), *out)
	}
}

func mustClean(s string) string {
	out, _, _ := sanitize.Clean(s, 0)
	return out
}

// urlSafe mirrors the renderer's check: plain http(s) ASCII, no control/space.
func urlSafe(u string) bool {
	if len(u) == 0 || len(u) > 300 {
		return false
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return false
	}
	for _, r := range u {
		if r < 0x21 || r > 0x7E {
			return false
		}
	}
	return true
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}
func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "sign: "+format+"\n", a...)
	os.Exit(1)
}
