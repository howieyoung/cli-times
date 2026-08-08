package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// ---- Unit tests: the exact payloads the harness probe emits must be REJECTED,
// and legitimate EN + 繁中 content must PASS. ----

func TestRejectsAttackPayloads(t *testing.T) {
	cases := map[string]string{
		"esc-sgr":        "\x1b[32mgreen\x1b[0m",
		"osc8":           "\x1b]8;;https://evil\x07text\x1b]8;;\x07",
		"osc52":          "\x1b]52;c;QQ==\x07",
		"title":          "\x1b]0;pwned\x07status",
		"cursor-clear":   "\x1b[2J\x1b[1;1Hcleared",
		"overwrite":      "\x1b[2F\x1b[1Goverwrote",
		"dcs":            "\x1bPdcs\x1b\\",
		"newline":        "line1\nline2",
		"carriage-ret":   "a\rb",
		"tab":            "a\tb",
		"del":            "a\x7fb",
		"c1-raw-85":      "a\x85b", // NEL as a real C1 codepoint
		"unicode-tag":    "vis\U000E0069ble",
		"bidi-rlo":       "start\u202Ereversed\u202Cend",
		"bidi-rlm":       "a\u200Fb",
		"zwsp":           "ze\u200Bro",
		"zwj":            "a\u200Db",
		"bom":            "\uFEFFhi",
		"lone-cont-byte": "a" + string([]byte{0x80}) + "b",
		"bare-emoji":     "ship it 🚀", // emoji not in allowlist (yet) → reject whole item
	}
	for name, in := range cases {
		if out, r, ok := Clean(in, 80); ok {
			t.Errorf("%s: expected REJECT, got ok=true out=%q reason=%q", name, out, r)
		}
	}
}

func TestAcceptsLegitContent(t *testing.T) {
	cases := map[string]string{
		"en-basic":      "OpenAI ships new model; benchmarks up 12%",
		"en-punct":      "Anthropic's Claude — now with “foo”… (beta)",
		"en-arrow":      "latency down 30% → faster tools",
		"zh-basic":      "OpenAI 發布新模型,開發者社群熱議",
		"zh-punct":      "重點:模型更新了(繁中全形標點)、值得一看。",
		"zh-mixed":      "Claude Code 2.1 發布 — 新增 statusLine 功能",
		"latin1":        "café résumé €99 ™",
		"one-two-three": "一二三四五",
		"nfkc-compose":  "café",       // e + combining acute → NFKC composes to precomposed é (safe)
		"nfkc-fullwidth": "ＡＢＣ！",         // fullwidth → NFKC folds to ABC! (allowlisted)
	}
	for name, in := range cases {
		out, r, ok := Clean(in, 200)
		if !ok {
			t.Errorf("%s: expected ACCEPT, got reject reason=%q in=%q", name, r, in)
			continue
		}
		if !utf8.ValidString(out) {
			t.Errorf("%s: output not valid UTF-8", name)
		}
	}
}

// Invisible-character smuggling: width-0 format/combining marks must be rejected
// even though they are visually "harmless" — they defeat "what you see is all
// there is" and can carry hidden payload into model context. (adversarial review
// findings #1 and #2 on the Go implementation.)
func TestRejectsInvisibleFormatAndCombining(t *testing.T) {
	cases := map[string]string{
		"soft-hyphen":     "hi\u00ADthere",       // Cf, width 0
		"stacked-dakuten": "A\u3099\u3099\u302A", // combining marks Mn/Mc
		"word-joiner":     "a\u2060b",            // WJ, Cf
		"invisible-times": "a\u2062b",            // invisible times, Cf
		"mongolian-fvs":   "a\u180Eb",            // Mongolian vowel separator, Cf
	}
	for name, in := range cases {
		if out, r, ok := Clean(in, 80); ok {
			t.Errorf("%s: expected REJECT of invisible char, got ok=true out=%q reason=%q", name, out, r)
		}
	}
}

// The C1 bug the adversarial review caught: raw-byte 0x80–0x9F scanning would
// eat CJK. This is the regression guard.
func TestCJKContinuationBytesNotMistakenForC1(t *testing.T) {
	// 一 = E4 B8 80 (contains 0x80), 二 = E4 BA 8C (0x8C), 三 = E4 B8 89 (0x89)
	if _, r, ok := Clean("一二三", 80); !ok {
		t.Fatalf("CJK with 0x80-range continuation bytes wrongly rejected: %q", r)
	}
}

// ---- Width truncation ----

func TestTruncationWidthCJK(t *testing.T) {
	// Each CJK char is 2 display columns; 5 chars = 10 cols. Cap at 6 → 3 chars.
	out, _, ok := Clean("一二三四五", 6)
	if !ok {
		t.Fatal("unexpected reject")
	}
	if w := uniseg.StringWidth(out); w > 6 {
		t.Errorf("width %d exceeds cap 6: %q", w, out)
	}
	if n := len([]rune(out)); n != 3 {
		t.Errorf("expected 3 CJK runes within 6 cols, got %d: %q", n, out)
	}
}

func TestTruncationNeverSplitsWideChar(t *testing.T) {
	// Cap at odd width 5 with 2-col chars → must stop at 4 cols (2 chars), not split.
	out, _, ok := Clean("東京東京", 5)
	if !ok {
		t.Fatal("unexpected reject")
	}
	if w := uniseg.StringWidth(out); w != 4 {
		t.Errorf("expected width 4 (no partial wide char), got %d: %q", w, out)
	}
}

// ---- Property-based fuzz: the three sanitizer invariants. ----

func FuzzClean(f *testing.F) {
	seeds := []string{
		"", "hello", "一二三", "\x1b[31mx", "a\x00b", "\U000E0069",
		"café", "a​b", strings.Repeat("東", 500), "→←↑↓",
		"\xff\xfe", "line\nbreak", "\x1b]52;c;QQ==\x07",
	}
	for _, s := range seeds {
		f.Add(s, 40)
	}
	f.Fuzz(func(t *testing.T, s string, maxCols int) {
		out, _, ok := Clean(s, maxCols)
		if !ok {
			if out != "" {
				t.Fatalf("rejected item must return empty string, got %q", out)
			}
			return
		}
		// INVARIANT 1: output ⊆ allowlist, with zero control/tag/bidi/zw runes.
		for _, ru := range out {
			if classify(ru) != OK {
				t.Fatalf("accepted output contains disallowed rune %U in %q", ru, out)
			}
		}
		// INVARIANT 2: single line (no control means no \n/\r — belt and braces).
		if strings.ContainsAny(out, "\n\r") {
			t.Fatalf("accepted output is multi-line: %q", out)
		}
		// INVARIANT 3: width within cap.
		if maxCols > 0 {
			if w := uniseg.StringWidth(out); w > maxCols {
				t.Fatalf("width %d exceeds cap %d: %q", w, maxCols, out)
			}
		}
		// INVARIANT 4: always valid UTF-8.
		if !utf8.ValidString(out) {
			t.Fatalf("accepted output is invalid UTF-8: %q", out)
		}
	})
}
