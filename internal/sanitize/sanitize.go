// Package sanitize is the security core of Cli Times: it turns an untrusted
// feed string into something that can only ever be *displayed*, never executed.
//
// Design:
//   - Rejection, not stripping: any disallowed rune fails the WHOLE item.
//   - Positive allowlist: only explicitly permitted codepoints survive, so
//     "what you can see is all there is" — no invisible smuggling.
//   - Runs identically here and in the renderer (defense in depth).
//
// The one function callers use is Clean. It never emits an escape byte; colour
// is applied by the renderer from a constant table, never from feed bytes.
package sanitize

import (
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
)

// Reason explains why an item was rejected (for harness/telemetry, never shown).
type Reason string

const (
	OK          Reason = ""
	InvalidUTF8 Reason = "invalid-utf8"
	ControlByte Reason = "control-or-c1"
	UnicodeTag  Reason = "unicode-tag"
	BidiControl Reason = "bidi-control"
	ZeroWidth   Reason = "zero-width"
	Invisible   Reason = "format-or-combining" // width-0 format/combining marks (SHY, dakuten, …)
	NotAllowed  Reason = "not-in-allowlist"
	Empty       Reason = "empty-after-clean"
)

// Clean validates s against the allowlist and, if every rune passes, returns the
// NFKC-normalised text truncated to maxCols display columns at a grapheme
// boundary. ok is false (and out is "") if the item must be rejected wholesale.
//
// maxCols <= 0 is treated as "no width limit" (validation still applies).
func Clean(s string, maxCols int) (out string, r Reason, ok bool) {
	// 1. Normalise FIRST (review finding #4 ordering), then validate codepoints.
	s = norm.NFKC.String(s)

	// 2. Reject on the first disallowed rune. We decode explicitly so a lone
	//    UTF-8 continuation byte (RuneError, width 1) is caught, not silently
	//    replaced — critical because raw-byte scanning for 0x80–0x9F would eat
	//    the continuation bytes of every CJK character (一 = E4 B8 80).
	for i := 0; i < len(s); {
		ru, size := utf8.DecodeRuneInString(s[i:])
		if ru == utf8.RuneError && size == 1 {
			return "", InvalidUTF8, false
		}
		if rr := classify(ru); rr != OK {
			return "", rr, false
		}
		i += size
	}

	// 3. Truncate to width at grapheme boundaries (東亞全形字寬 = 2).
	out = truncateCols(s, maxCols)
	if out == "" {
		return "", Empty, false
	}
	return out, OK, true
}

// classify returns OK if the rune is displayable-and-safe, else the reason.
func classify(ru rune) Reason {
	switch {
	case ru <= 0x1F || ru == 0x7F: // C0 controls + DEL (includes \n, \r, \t, ESC)
		return ControlByte
	case ru >= 0x80 && ru <= 0x9F: // C1 controls — at CODEPOINT level, never raw byte
		return ControlByte
	case ru >= 0xE0000 && ru <= 0xE007F: // Unicode Tags — invisible to humans, legible to models
		return UnicodeTag
	case isBidi(ru):
		return BidiControl
	case isZeroWidth(ru):
		return ZeroWidth
	// Reject by CATEGORY, before the allowlist: format chars (Cf: soft hyphen,
	// zero-width, tags…), combining marks (Mn/Mc/Me: dakuten, tone marks — can
	// stack invisibly on one column), other controls/separators. This is the
	// principled backstop that closes the "invisible smuggling" class rather
	// than enumerating exceptions. Precomposed forms (é, が) are Letters and pass.
	case unicode.In(ru, unicode.Cf, unicode.Cc, unicode.Cs, unicode.Co,
		unicode.Zl, unicode.Zp, unicode.Mn, unicode.Mc, unicode.Me):
		return Invisible
	case !allowed(ru):
		return NotAllowed
	}
	return OK
}

func isBidi(ru rune) bool {
	switch ru {
	case 0x200E, 0x200F, // LRM, RLM
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // LRE RLE PDF LRO RLO
		0x2066, 0x2067, 0x2068, 0x2069: // LRI RLI FSI PDI
		return true
	}
	return false
}

func isZeroWidth(ru rune) bool {
	switch ru {
	case 0x200B, 0x200C, 0x200D, // ZWSP ZWNJ ZWJ
		0xFEFF: // BOM / ZWNBSP
		return true
	}
	return false
}

// allowed is the POSITIVE allowlist. Anything not listed is rejected, so adding
// a new character class is a deliberate, reviewable act — never an accident.
func allowed(ru rune) bool {
	switch {
	case ru >= 0x20 && ru <= 0x7E: // printable ASCII
		return true
	case ru == 0x00A0: // no-break space (allowed, not a control)
		return true
	case ru >= 0x00A1 && ru <= 0x00FF: // Latin-1 supplement letters/symbols (é, ü, ©, °, …)
		return true
	case ru >= 0x2010 && ru <= 0x2027: // general punctuation: – — ' ' " " … ‹ etc.
		return true
	case ru == 0x20AC || ru == 0x2122: // € ™
		return true
	case ru == 0x2192 || ru == 0x2190 || ru == 0x2191 || ru == 0x2193: // → ← ↑ ↓ arrows
		return true
	case ru >= 0x3000 && ru <= 0x303F: // CJK symbols & punctuation 、。「」《》
		return true
	case ru >= 0x3040 && ru <= 0x30FF: // Hiragana + Katakana
		return true
	case ru >= 0x3400 && ru <= 0x4DBF: // CJK Ext-A
		return true
	case ru >= 0x4E00 && ru <= 0x9FFF: // CJK Unified Ideographs (common)
		return true
	case ru >= 0xF900 && ru <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case ru >= 0xFF01 && ru <= 0xFF60: // Fullwidth forms (！？（）)
		return true
	case ru >= 0x20000 && ru <= 0x2A6DF: // CJK Ext-B (rare names)
		return true
	}
	return false
}

// truncateCols cuts s to at most maxCols monospace display columns, never
// splitting a grapheme cluster and never leaving a partial wide character.
func truncateCols(s string, maxCols int) string {
	if maxCols <= 0 {
		return s
	}
	var (
		g     = uniseg.NewGraphemes(s)
		width int
		out   []byte
	)
	for g.Next() {
		w := g.Width()
		if width+w > maxCols {
			break
		}
		width += w
		out = append(out, []byte(g.Str())...)
	}
	return string(out)
}
