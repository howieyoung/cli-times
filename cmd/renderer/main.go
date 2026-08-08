// Command renderer is the statusLine script: read cache → verify → pick a line →
// sanitize → truncate → print ONE line. It is deliberately incapable of harm:
//
//   - No network, no shell exec, no file writes (reads one cache file only).
//   - Does NOT read Claude Code's stdin JSON (transcript path, cwd, repo — we
//     don't even look), so there is nothing sensitive to leak. Width comes from
//     the COLUMNS env var, bounds-checked.
//   - Re-verifies the signed envelope with the PINNED key (defense in depth).
//   - Any error at any step prints nothing (fail-closed). Errors never surface
//     an escape byte; colour comes from a constant table, never from feed bytes.
//   - The "Ad:" label on sponsored lines is prepended HERE from a constant,
//     never trusted from feed text (so a key compromise can't ship an unlabeled ad).
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howieyoung/cli-times/internal/feed"
	"github.com/howieyoung/cli-times/internal/sanitize"
	"github.com/rivo/uniseg"
)

// Pinned at build time via -ldflags "-X main.pinnedKeyB64=... -X main.pinnedKeyID=...".
// Empty falls back to env (dev only). In production these are compiled in so the
// updater cannot swap the key.
var (
	pinnedKeyB64 string
	pinnedKeyID  string
)

// Fixed SGR from a constant table — the ONLY escape bytes we ever emit.
const (
	sgrDim   = "\x1b[2m"
	sgrReset = "\x1b[0m"
	adLabel = "Ad: " // renderer-enforced; never from feed
	// brandStatic is the fallback provenance mark when animation is disabled.
	// PLACEHOLDER — swap for the real logo/wordmark later.
	brandStatic = "◆"
	// brandName is the short wordmark shown after the animated icon, so the line
	// reads e.g. "⠹ CT: <headline> · <source>". Renderer constant — unspoofable.
	brandName = "CT: "
)

// brandFrames is the ANIMATED provenance icon: one frame per wall-clock second
// (Claude Code re-runs the statusLine on a refreshInterval, min 1s — so ~1 fps
// is the platform ceiling; this is a slow, tasteful spinner, not Claude's own
// sub-second one). Each frame is a renderer constant — it can never be spoofed
// by feed content, and animation adds NO new capability (still stateless, no
// writes, no network; the frame is derived purely from the clock). A tiny
// braille spinner reads as "live/fresh". Swap this slice for the real animated
// logo later. Set CLI_TIMES_NO_ANIM=1 to fall back to the static mark.
var brandFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// brandIcon returns the provenance mark for this instant, followed by a space.
// Frame is chosen from the wall clock so repeated invocations within the same
// second are stable (no flicker), advancing once per second.
func brandIcon(now time.Time) string {
	if os.Getenv("CLI_TIMES_NO_ANIM") != "" || len(brandFrames) == 0 {
		return brandStatic + " "
	}
	f := int(now.Unix()) % len(brandFrames)
	if f < 0 {
		f = 0
	}
	return brandFrames[f] + " "
}

const (
	slotSeconds  = 8  // rotate line every N seconds (deterministic by wall clock)
	defaultCols  = 80 // when COLUMNS is unset/hostile
	maxCols      = 200
	reserveForCC = 12 // leave room for Claude Code's own footer/truncation
)

func main() {
	// Absolutely never crash the user's statusLine. Any panic → print nothing.
	defer func() { _ = recover() }()

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--brief", "today", "brief":
			brief(os.Stdout)
			return
		case "--version", "version":
			fmt.Println("cli-times " + version)
			return
		}
	}

	line, ok := render(time.Now())
	if !ok {
		return // fail-closed: print nothing
	}
	os.Stdout.WriteString(line)
}

var version = "0.1.0-dev"

// brief prints the full day's feed for the `cli-times today` shell command: one
// entry per line with attribution (著作權法 §64) and a plain-text URL. This is a
// SHELL command (terminal-only), NOT a Claude Code slash command — its output
// never enters model context. All fields are sanitized; URLs are validated to be
// plain https ASCII (no escapes) and are the ONE place a full link is shown.
func brief(w interface {
	WriteString(string) (int, error)
}) {
	pub, kid, ok := loadPinnedKey()
	if !ok {
		w.WriteString("cli-times: not configured\n")
		return
	}
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		w.WriteString("cli-times: no feed yet\n")
		return
	}
	b, err := feed.Verify(raw, map[string]ed25519.PublicKey{kid: pub}, -1, time.Now())
	if err != nil || b == nil || len(b.Lines) == 0 {
		w.WriteString("cli-times: no valid feed\n")
		return
	}
	w.WriteString(fmt.Sprintf("Cli Times — AI 日報 (v%d, %d 則)\n\n", b.Version, len(b.Lines)))
	for _, ln := range b.Lines {
		text, _, good := sanitize.Clean(ln.Text, 0) // no width cap in brief
		if !good {
			continue
		}
		prefix := "• "
		if ln.Sponsored {
			prefix = "• " + adLabel
		}
		w.WriteString(prefix + text + "\n")
		var meta []string
		if src, _, sok := sanitize.Clean(ln.Source, 0); sok && src != "" {
			meta = append(meta, src)
		}
		if au, _, aok := sanitize.Clean(ln.Author, 0); aok && au != "" {
			meta = append(meta, au)
		}
		if u := urlSafe(ln.URL); u != "" {
			meta = append(meta, u)
		}
		if len(meta) > 0 {
			w.WriteString("  " + strings.Join(meta, "  ·  ") + "\n")
		}
		w.WriteString("\n")
	}
}

// urlSafe returns the URL only if it is a plain http(s) ASCII string with no
// control/space characters and a sane length — else "". URLs are never emitted
// into the ticker; this is for the brief command only.
func urlSafe(u string) string {
	if len(u) == 0 || len(u) > 300 {
		return ""
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return ""
	}
	for _, r := range u {
		if r < 0x21 || r > 0x7E { // printable ASCII, no space, no control
			return ""
		}
	}
	return u
}

func render(now time.Time) (string, bool) {
	pub, kid, ok := loadPinnedKey()
	if !ok {
		return "", false
	}
	raw, err := os.ReadFile(cachePath())
	if err != nil {
		return "", false
	}
	b, err := feed.Verify(raw, map[string]ed25519.PublicKey{kid: pub}, -1, now)
	if err != nil || b == nil || len(b.Lines) == 0 {
		return "", false
	}

	cols := columns() - reserveForCC
	if cols < 8 {
		cols = 8
	}

	const sep = "  · "

	// Deterministic rotation by wall-clock slot; try each line until one passes
	// sanitisation so a single bad item never blanks the whole ticker.
	n := len(b.Lines)
	start := int(now.Unix()/slotSeconds) % n
	for off := 0; off < n; off++ {
		ln := b.Lines[(start+off)%n]
		prefix := brandIcon(now) + brandName // animated mark + wordmark, renderer-enforced
		if ln.Sponsored {
			prefix += adLabel
		}
		avail := cols - uniseg.StringWidth(prefix)
		if avail < 4 {
			continue
		}

		// Validate the item first — a malicious headline skips the WHOLE item.
		if _, _, ok := sanitize.Clean(ln.Text, avail); !ok {
			continue
		}

		// SOURCE IS GUARANTEED: reserve it first, the headline takes the
		// remainder and is shortened as much as needed. Only in the degenerate
		// case where the terminal is too narrow to fit both do we show the
		// source alone (never drop the source to keep the headline).
		src, _, srcOK := sanitize.Clean(ln.Source, 28)
		if srcOK && src != "" {
			srcCost := uniseg.StringWidth(sep) + uniseg.StringWidth(src)
			if avail-srcCost >= 1 {
				text, _, _ := sanitize.Clean(ln.Text, avail-srcCost)
				return sgrDim + prefix + text + sep + src + sgrReset, true
			}
			// Too narrow for headline+source → source wins, headline dropped.
			s, _, _ := sanitize.Clean(ln.Source, avail)
			return sgrDim + prefix + s + sgrReset, true
		}

		// No usable source (shouldn't happen for our feed) → headline only.
		text, _, _ := sanitize.Clean(ln.Text, avail)
		return sgrDim + prefix + text + sgrReset, true
	}
	return "", false
}

func loadPinnedKey() (ed25519.PublicKey, string, bool) {
	b64, kid := pinnedKeyB64, pinnedKeyID
	if b64 == "" { // dev fallback only
		b64 = os.Getenv("CLI_TIMES_PUBKEY")
		kid = os.Getenv("CLI_TIMES_KID")
	}
	if b64 == "" || kid == "" {
		return nil, "", false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, "", false
	}
	return ed25519.PublicKey(raw), kid, true
}

func cachePath() string {
	if d := os.Getenv("CLI_TIMES_DIR"); d != "" {
		return filepath.Join(d, "cache.json")
	}
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "cli-times", "cache.json")
}

// columns parses COLUMNS as UNTRUSTED input: non-numeric / 0 / negative / huge
// all collapse to a safe default and clamp.
func columns() int {
	c, err := strconv.Atoi(os.Getenv("COLUMNS"))
	if err != nil || c <= 0 {
		return defaultCols
	}
	if c > maxCols {
		return maxCols
	}
	return c
}
