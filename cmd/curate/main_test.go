package main

import "testing"

func TestNumbersSupported(t *testing.T) {
	cases := []struct {
		text, src string
		want      bool
	}{
		{"DeepSeek hits 89% on ARC-AGI-1", "deepseek v4 flash 89% arc-agi", true},     // number present
		{"Model scores 92% on the benchmark", "deepseek v4 flash 89% arc-agi", false}, // 92 not in source → flag
		{"AMD acquires Taalas chip startup", "amd acquires taalas", true},             // no numbers → fine
		{"cut cost over 30%", "smart router cuts cost by more than 30% per task", true},
		{"raised $150M Series C", "startup raised $50m", false}, // 150 not in source
	}
	for _, c := range cases {
		if got := numbersSupported(c.text, c.src); got != c.want {
			t.Errorf("numbersSupported(%q, %q) = %v, want %v", c.text, c.src, got, c.want)
		}
	}
}

func TestParseAndFlagObjectWrapper(t *testing.T) {
	// OpenAI json_object mode returns an {"items":[...]} wrapper. extractJSONArray
	// must recover the array and flagReason must still run per line.
	raw := `{"items":[
	  {"t":"Oracle restricts AI-generated code contributions to OpenJDK","src":"HN","url":"https://example.com/a","lang":"en","evidence":"Oracle bans AI code","confidence":"high"},
	  {"t":"Model claims 92% on benchmark","src":"HN","url":"https://example.com/b","lang":"en","evidence":"scores highly","confidence":"high"},
	  {"t":"Anthropic ships Claude update","src":"HN","url":"https://example.com/c","lang":"en","evidence":"anthropic ships claude","confidence":"high"}
	]}`
	srcText := map[string]string{
		"https://example.com/a": "oracle bans ai-generated code from openjdk",
		"https://example.com/b": "some model scores highly on reasoning", // no "92"
		"https://example.com/c": "anthropic ships claude update",
	}
	lines, kept, err := parseAndFlag(raw, srcText)
	if err != nil {
		t.Fatalf("parseAndFlag error: %v", err)
	}
	if len(lines) != 3 || len(kept) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if lines[0].Review != "" {
		t.Errorf("line 0 (clean) should be cleared, got %q", lines[0].Review)
	}
	if lines[1].Review == "" {
		t.Error("line 1 (92% not in source) should be flagged")
	}
	if lines[2].Review != "" {
		t.Errorf("line 2 (Anthropic/Claude) is now a KEPT category, should be cleared, got %q", lines[2].Review)
	}
}

func TestAttributeSource(t *testing.T) {
	cases := []struct{ modelSrc, url, want string }{
		{"Hacker News", "https://arcprize.org/results/x", "ARC Prize"}, // discovery → derive
		{"HN", "https://blog.cloudflare.com/kitesurf", "Cloudflare"},   // suffix match
		{"", "https://www.theregister.com/a", "The Register"},          // blank → derive
		{"The Register", "https://arstechnica.com/x", "The Register"},  // real byline → keep model's
		{"Hacker News", "https://genesisopenmodels.anl.gov/", "Anl"},   // unknown host → generic fallback
	}
	for _, c := range cases {
		if got := attributeSource(c.modelSrc, c.url); got != c.want {
			t.Errorf("attributeSource(%q, %q) = %q, want %q", c.modelSrc, c.url, got, c.want)
		}
	}
}

func TestTitleSimilar(t *testing.T) {
	// Lexical dedup catches NEAR-IDENTICAL headlines (syndication / reposts).
	a := titleTokens("OpenAI launches GPT-5.6 Sol reasoning upgrade for ChatGPT")
	b := titleTokens("OpenAI launches GPT-5.6 Sol reasoning upgrade for ChatGPT users")
	if !titleSimilar(a, b) {
		t.Error("near-identical reposted headline should be detected as duplicate")
	}
	// Distinct products from the same vendor must NOT merge (Sol vs Luna).
	c := titleTokens("OpenAI launches GPT-5.6 Sol reasoning model for developers")
	d := titleTokens("OpenAI launches GPT-5.6 Luna image model for developers")
	if titleSimilar(c, d) {
		t.Error("distinct products (Sol vs Luna) must NOT be merged")
	}
	// Unrelated stories must not merge.
	e := titleTokens("Oracle bars AI-generated code contributions from OpenJDK project")
	if titleSimilar(a, e) {
		t.Error("unrelated stories must not merge")
	}
}

func TestDedupeDropsNearDuplicateTitles(t *testing.T) {
	in := []candidate{
		{Title: "OpenAI launches GPT-5.6 Sol reasoning upgrade for ChatGPT", URL: "https://a.com/x", Score: 300},
		{Title: "OpenAI launches GPT-5.6 Sol reasoning upgrade for ChatGPT users", URL: "https://b.com/y", Score: 90},
		{Title: "Oracle bars AI-generated contributions from OpenJDK project", URL: "https://c.com/z", Score: 498},
	}
	out := dedupeAndRank(in)
	if len(out) != 2 {
		t.Fatalf("want 2 after near-dup title merge, got %d: %+v", len(out), out)
	}
	for _, c := range out {
		if c.Score == 90 {
			t.Error("lower-ranked near-duplicate headline should have been dropped")
		}
	}
}

func TestFlagReason(t *testing.T) {
	src := "openai ships gpt-5.5 with faster tool calls" // no numbers
	// clean, high confidence, non-sensitive → cleared
	if r := flagReason("OpenAI ships GPT-5.5 with faster tool calls", src, "high", false); r != "" {
		t.Errorf("clean line should not be flagged, got %q", r)
	}
	// fabricated number → flagged
	if r := flagReason("GPT-5.5 is 40% faster", src, "high", false); r == "" {
		t.Error("unsupported number should be flagged")
	}
	// Anthropic/Claude → NO LONGER flagged (wanted editorial category, Howie's call)
	if r := flagReason("Anthropic ships Claude Code 2.2", "anthropic ships claude code 2.2", "high", false); r != "" {
		t.Errorf("Anthropic/Claude should be kept now, got %q", r)
	}
	// medium confidence → KEPT (only low is dropped, to avoid a thin feed)
	if r := flagReason("Some tool launched", "some tool launched today", "medium", false); r != "" {
		t.Errorf("medium confidence should be kept, got %q", r)
	}
	// low confidence → flagged
	if r := flagReason("Some tool launched", "some tool launched today", "low", false); r == "" {
		t.Error("low confidence should be flagged")
	}
	// sponsored → flagged
	if r := flagReason("Vendor launches SDK", "vendor launches sdk", "high", true); r == "" {
		t.Error("sponsored line should be flagged")
	}
}
