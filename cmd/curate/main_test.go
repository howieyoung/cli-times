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
	if lines[2].Review == "" {
		t.Error("line 2 (Anthropic/Claude) should be flagged")
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
	// sensitive vendor → flagged
	if r := flagReason("Anthropic ships Claude Code 2.2", "anthropic ships claude code 2.2", "high", false); r == "" {
		t.Error("Anthropic/Claude topic should be flagged for human glance")
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
