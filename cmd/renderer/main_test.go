package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howieyoung/cli-times/internal/feed"
	"github.com/rivo/uniseg"
)

// setup writes a signed cache into a temp dir and points the renderer's env at
// it (dev-fallback pinned key via env). Returns the injected pinned key id.
func setup(t *testing.T, lines []feed.Line, expires time.Time) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(feed.Bundle{
		Version: 100, Issued: time.Unix(1_800_000_000, 0), Expires: expires, Lines: lines,
	})
	sig := ed25519.Sign(priv, payload)
	env, _ := json.Marshal(feed.Envelope{
		Payload: payload, Signature: base64.StdEncoding.EncodeToString(sig), KeyID: "k1",
	})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cache.json"), env, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLI_TIMES_DIR", dir)
	t.Setenv("CLI_TIMES_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	t.Setenv("CLI_TIMES_KID", "k1")
	t.Setenv("COLUMNS", "120")
	pinnedKeyB64, pinnedKeyID = "", "" // force env fallback
}

func hasFeedEscape(s string) bool {
	// Strip our known-good wrapper, then any remaining ESC is a leak.
	s = strings.TrimPrefix(s, sgrDim)
	s = strings.TrimSuffix(s, sgrReset)
	return strings.ContainsRune(s, 0x1b)
}

func TestRenderRotatesAndSkipsMalicious(t *testing.T) {
	malicious := "EVIL \x1b[2J\x1b]52;c;QQ==\x07 wiped screen"
	lines := []feed.Line{
		{Text: "OpenAI ships new model", Source: "hn", Lang: "en"},
		{Text: malicious, Source: "attacker", Lang: "en"},
		{Text: "Anthropic 發布 Claude Code 2.2", Source: "官方部落格", Lang: "zh-TW"},
		{Text: "Vercel AI SDK v6", Source: "vercel.com", Lang: "en", Sponsored: true},
	}
	setup(t, lines, time.Unix(4_000_000_000, 0))

	seen := map[string]bool{}
	sawAd := false
	for slot := 0; slot < 8; slot++ {
		now := time.Unix(int64(slot*8), 0)
		out, ok := render(now)
		if !ok {
			t.Fatalf("slot %d: expected a line, got fail-closed", slot)
		}
		if hasFeedEscape(out) {
			t.Fatalf("slot %d: feed-originated escape leaked: %q", slot, out)
		}
		if !strings.Contains(out, brandIcon(now)) {
			t.Fatalf("slot %d: brand icon missing (provenance): %q", slot, out)
		}
		if !strings.Contains(out, brandName) {
			t.Fatalf("slot %d: CT: wordmark missing (provenance): %q", slot, out)
		}
		if strings.Contains(out, "wiped screen") || strings.Contains(out, "EVIL") {
			t.Fatalf("slot %d: malicious line surfaced: %q", slot, out)
		}
		if strings.Count(out, "\n") != 0 {
			t.Fatalf("slot %d: multi-line output: %q", slot, out)
		}
		if strings.Contains(out, adLabel) {
			sawAd = true
			if !strings.Contains(out, "Vercel") {
				t.Fatalf("Ad label on non-sponsored line: %q", out)
			}
		}
		seen[out] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected rotation across slots, saw only: %v", seen)
	}
	if !sawAd {
		t.Fatalf("sponsored line never rendered with %q prefix", adLabel)
	}
}

// Regression: the source/publisher label must actually show at normal terminal
// widths (the earlier logic reserved no room for it, so a long headline always
// crowded it out). Verify the source appears at 80 and 100 columns.
func TestRenderShowsSourceAtNormalWidth(t *testing.T) {
	lines := []feed.Line{
		{Text: "US Energy Dept. opens Genesis initiative for open-weight science AI models", Source: "US DOE / Argonne", Lang: "en"},
	}
	setup(t, lines, time.Unix(4_000_000_000, 0))
	// Source must appear across the whole realistic width range, including narrow.
	for _, w := range []string{"40", "60", "80", "100", "120"} {
		t.Setenv("COLUMNS", w)
		out, ok := render(time.Unix(0, 0))
		if !ok {
			t.Fatalf("COLUMNS=%s: expected a line", w)
		}
		if !strings.Contains(out, "US DOE / Argonne") {
			t.Fatalf("COLUMNS=%s: source label missing from ticker: %q", w, out)
		}
		body := strings.TrimPrefix(strings.TrimSuffix(out, sgrReset), sgrDim)
		if uniseg.StringWidth(body) > maxCols {
			t.Fatalf("COLUMNS=%s: line too wide: %q", w, out)
		}
	}
}

// Source-priority guarantee: even at a punishing width the source survives and
// it is the HEADLINE that gets sacrificed (Howie's explicit rule).
func TestRenderSourceWinsWhenNarrow(t *testing.T) {
	lines := []feed.Line{
		{Text: "A very long headline that cannot possibly fit in a tiny terminal window", Source: "The Register", Lang: "en"},
	}
	setup(t, lines, time.Unix(4_000_000_000, 0))
	// At a comfortable-but-modest width the FULL source must appear.
	t.Setenv("COLUMNS", "50")
	out, ok := render(time.Unix(0, 0))
	if !ok || !strings.Contains(out, "The Register") {
		t.Fatalf("width 50: full source must appear: ok=%v %q", ok, out)
	}
	// At a punishing width the headline is sacrificed and the source may itself
	// be truncated — but the source (its leading part) still wins over the title.
	t.Setenv("COLUMNS", "28")
	out, ok = render(time.Unix(0, 0))
	if !ok {
		t.Fatal("expected a line even when very narrow")
	}
	if !strings.Contains(out, "The Reg") {
		t.Fatalf("very narrow: source must still appear (truncation ok, priority not): %q", out)
	}
	if strings.Contains(out, "very long headline") {
		t.Fatalf("very narrow: headline should be sacrificed for the source: %q", out)
	}
}

// The provenance icon animates: over a full frame cycle we see more than one
// distinct frame, each is a single renderer constant, and animation changes
// nothing about the content/source guarantees or the width.
func TestBrandIconAnimates(t *testing.T) {
	lines := []feed.Line{{Text: "US Energy Dept. opens Genesis initiative", Source: "US DOE / Argonne", Lang: "en"}}
	setup(t, lines, time.Unix(4_000_000_000, 0))
	t.Setenv("COLUMNS", "100")

	seenFrames := map[string]bool{}
	for s := 0; s < len(brandFrames); s++ {
		now := time.Unix(int64(s), 0)
		out, ok := render(now)
		if !ok {
			t.Fatalf("second %d: expected a line", s)
		}
		if !strings.HasPrefix(out, sgrDim+brandFrames[s]+" ") {
			t.Fatalf("second %d: expected frame %q at head, got %q", s, brandFrames[s], out)
		}
		if !strings.Contains(out, "US DOE / Argonne") {
			t.Fatalf("second %d: source must remain visible while animating: %q", s, out)
		}
		seenFrames[brandFrames[s]] = true
	}
	if len(seenFrames) < 2 {
		t.Fatalf("icon did not animate across the cycle: %v", seenFrames)
	}

	// Static fallback when animation disabled.
	t.Setenv("CLI_TIMES_NO_ANIM", "1")
	out, ok := render(time.Unix(3, 0))
	if !ok || !strings.Contains(out, brandStatic) {
		t.Fatalf("CLI_TIMES_NO_ANIM: expected static mark %q, got ok=%v %q", brandStatic, ok, out)
	}
}

func TestRenderSponsoredLabelIsRendererEnforced(t *testing.T) {
	// Even if feed text tries to look un-sponsored, the flag drives the label.
	lines := []feed.Line{{Text: "totally organic content", Source: "x", Lang: "en", Sponsored: true}}
	setup(t, lines, time.Unix(4_000_000_000, 0))
	out, ok := render(time.Unix(0, 0))
	if !ok {
		t.Fatal("expected render")
	}
	if !strings.Contains(out, adLabel) {
		t.Fatalf("sponsored line missing %q label: %q", adLabel, out)
	}
}

func TestRenderFailClosedOnExpired(t *testing.T) {
	lines := []feed.Line{{Text: "stale", Source: "x", Lang: "en"}}
	setup(t, lines, time.Unix(1, 0)) // long expired vs render time below
	if out, ok := render(time.Unix(3_000_000_000, 0)); ok {
		t.Fatalf("expired bundle should fail-closed, got %q", out)
	}
}

func TestRenderFailClosedOnMissingCache(t *testing.T) {
	setup(t, []feed.Line{{Text: "hi", Source: "x", Lang: "en"}}, time.Unix(4_000_000_000, 0))
	os.Remove(filepath.Join(os.Getenv("CLI_TIMES_DIR"), "cache.json"))
	if out, ok := render(time.Unix(0, 0)); ok {
		t.Fatalf("missing cache should fail-closed, got %q", out)
	}
}

func TestRenderFailClosedWhenAllLinesRejected(t *testing.T) {
	lines := []feed.Line{
		{Text: "\x1b[2Jonly-evil", Source: "x", Lang: "en"},
		{Text: "also\x00evil", Source: "x", Lang: "en"},
	}
	setup(t, lines, time.Unix(4_000_000_000, 0))
	if out, ok := render(time.Unix(0, 0)); ok {
		t.Fatalf("all-rejected bundle should fail-closed, got %q", out)
	}
}

func TestColumnsHostileInput(t *testing.T) {
	for _, v := range []string{"", "0", "-5", "abc", "999999", "  "} {
		t.Setenv("COLUMNS", v)
		got := columns()
		if got < 8 || got > maxCols {
			t.Fatalf("COLUMNS=%q → %d, out of safe range", v, got)
		}
	}
}
