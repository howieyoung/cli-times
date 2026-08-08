// Command curate is the semi-automated editorial pipeline (see the content policy):
//
//	crawl 🟢 sources (HN API + official/reputable RSS) → AI-filter + recency →
//	a model picks + drafts one-line items with a source-fact citation + self-rated
//	confidence → deterministic RISK FLAGGING (unverified number / low confidence /
//	sponsored) → write a DRAFT bundle (flagged lines carry a "review" marker) + a
//	review file. It NEVER signs and NEVER publishes.
//
// Two downstream modes consume the draft:
//   - AUTO (CI): `sign -strip-review` DROPS any still-flagged line and publishes
//     the rest — so risky content never ships without a human, unattended.
//   - HUMAN: an editor reads review-<date>.md, verifies each ⚠ line, and deletes
//     its "review" field (or the line); build.sh refuses to sign while any remains.
//
// The crawl needs no credentials; drafting calls a pluggable Drafter — OpenAI by
// default (OPENAI_API_KEY) or -provider anthropic (ANTHROPIC_API_KEY). The model
// is an UNTRUSTED, swappable component: everything that guards safety (sanitize,
// urlSafe, flagReason, the review gate, signing) runs on its output identically,
// so switching providers changes nothing a CLI reader ever sees.
// Sources are the content policy's 🟢 tier only (HN API + RSS of official vendor
// blogs / reputable outlets). X / Reddit / Threads are never crawled here.
package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/howieyoung/cli-times/internal/feed"
	"github.com/howieyoung/cli-times/internal/sanitize"
	"github.com/rivo/uniseg"
)

const (
	hnBest         = "https://hacker-news.firebaseio.com/v0/beststories.json"
	hnItem         = "https://hacker-news.firebaseio.com/v0/item/%d.json"
	maxHeadline    = 85
	recencyDays    = 14
	maxCands       = 120 // after dedup, how many ranked candidates to retain
	maxPromptCands = 80  // how many to actually feed the model / list in candidates.md
	spotCheck      = 3   // how many auto-cleared lines to mark for a random spot-check
)

// feeds are the 🟢 RSS/Atom sources. aiOnly=true means the whole feed is
// AI-scoped (no keyword filter); aiOnly=false feeds are keyword-filtered.
// Every URL below was verified live (HTTP 200 + valid RSS/Atom) on 2026-08-09.
// The crawler follows redirects and skips a dead feed gracefully (logged [warn]).
var feeds = []struct {
	URL, Label string
	aiOnly     bool
}{
	// Official vendor sources (highest editorial value — product/feature releases)
	{"https://openai.com/news/rss.xml", "OpenAI", true},
	{"https://blog.google/technology/ai/rss/", "Google AI", true},
	{"https://deepmind.google/blog/rss.xml", "DeepMind", true},
	{"https://huggingface.co/blog/feed.xml", "Hugging Face", true},
	{"https://about.fb.com/news/tag/ai/feed/", "Meta AI", true},
	{"https://blog.cloudflare.com/tag/ai/rss/", "Cloudflare", true},
	// Reputable outlets / research
	{"https://simonwillison.net/atom/everything/", "Simon Willison", true},
	{"https://www.theregister.com/software/ai_ml/headlines.atom", "The Register", true},
	{"https://arstechnica.com/ai/feed/", "Ars Technica", true},
	{"https://techcrunch.com/category/artificial-intelligence/feed/", "TechCrunch", true},
	{"https://www.theverge.com/rss/ai-artificial-intelligence/index.xml", "The Verge", true},
	{"https://www.technologyreview.com/topic/artificial-intelligence/feed", "MIT Tech Review", true},
	{"https://venturebeat.com/category/ai/feed/", "VentureBeat", true},
	{"https://bair.berkeley.edu/blog/feed.xml", "Berkeley AI Research", true},
	{"https://technews.tw/feed/", "科技新報 TechNews", false},
}

var keywords = []string{
	"ai", "llm", "gpt", "claude", "anthropic", "openai", "gemini", "deepseek",
	"model", "agent", "agentic", "ml", "neural", "inference", "transformer",
	"prompt", "rag", "fine-tun", "diffusion", "embedding", "vector db",
	"copilot", "cursor", "codex", "mcp", "hugging face", "pytorch", "tensor",
}

var skipHosts = []string{"x.com", "twitter.com", "reddit.com", "threads.net", "threads.com"}

type candidate struct {
	Title, URL, Source, Summary string
	When                        time.Time
	Score                       int
}

func main() {
	n := flag.Int("n", 18, "number of feed lines to draft (15-20 per 12h cycle)")
	scan := flag.Int("scan", 100, "how many HN best-stories to scan")
	minScore := flag.Int("min-score", 40, "minimum HN score to consider")
	outDir := flag.String("out", "feed-bundles", "output directory")
	skipRSS := flag.Bool("no-rss", false, "skip RSS sources (HN only)")
	provider := flag.String("provider", "openai", "drafter provider: openai|anthropic")
	modelID := flag.String("model", "", "model id override (default per provider)")
	flag.Parse()

	// Build the drafter up front so a bad -provider fails before we spend a crawl.
	d, err := newDrafter(*provider, *modelID)
	if err != nil {
		fail("%v", err)
	}

	var (
		mu   sync.Mutex
		all  []candidate
		wg   sync.WaitGroup
		errs []string
	)
	add := func(c []candidate) { mu.Lock(); all = append(all, c...); mu.Unlock() }
	adderr := func(s string) { mu.Lock(); errs = append(errs, s); mu.Unlock() }

	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := crawlHN(*scan, *minScore)
		if err != nil {
			adderr("HN: " + err.Error())
			return
		}
		add(c)
	}()
	if !*skipRSS {
		for _, f := range feeds {
			wg.Add(1)
			go func(u, label string, aiOnly bool) {
				defer wg.Done()
				c, err := crawlRSS(u, label, aiOnly)
				if err != nil {
					adderr(label + ": " + err.Error())
					return
				}
				add(c)
			}(f.URL, f.Label, f.aiOnly)
		}
	}
	wg.Wait()

	cands := dedupeAndRank(all)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "[warn] feed skipped — %s\n", e)
	}
	if len(cands) == 0 {
		fail("no AI-related candidates found")
	}
	fmt.Fprintf(os.Stderr, "[crawl] %d candidates (HN + %d RSS feeds)\n", len(cands), len(feeds))

	date := time.Now().UTC().Format("2006-01-02")
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail("mkdir: %v", err)
	}
	writeCandidates(filepath.Join(*outDir, "candidates-"+date+".md"), cands)

	if os.Getenv(d.KeyEnv()) == "" {
		fmt.Fprintf(os.Stderr, "[draft] %s unset — wrote candidates only (provider=%s).\n", d.KeyEnv(), d.Name())
		fmt.Fprintf(os.Stderr, "        Set it and re-run to auto-draft, or hand-write ~%d lines.\n", *n)
		return
	}

	system, user, srcText := buildEditorPrompt(cands, *n)
	raw, err := d.Draft(context.Background(), system, user, *n)
	if err != nil {
		fail("draft (%s): %v", d.Name(), err)
	}
	lines, drafted, err := parseAndFlag(raw, srcText)
	if err != nil {
		fail("draft: %v", err)
	}
	flagged := 0
	for _, ln := range lines {
		if ln.Review != "" {
			flagged++
		}
	}
	fmt.Fprintf(os.Stderr, "[draft] %s: %d lines (%d auto-cleared, %d flagged for review)\n", d.Name(), len(lines), len(lines)-flagged, flagged)
	if len(lines) < *n {
		fmt.Fprintf(os.Stderr, "[warn] requested %d lines but only %d were usable — re-run or widen sources (not padded with weak items)\n", *n, len(lines))
	}

	b := feed.Bundle{
		Version: int(time.Now().Unix()),
		Issued:  time.Now().UTC(),
		Expires: time.Now().UTC().Add(48 * time.Hour),
		Lines:   lines,
	}
	draftPath := filepath.Join(*outDir, "draft-"+date+".json")
	draftJSON, _ := json.MarshalIndent(b, "", "  ")
	if err := os.WriteFile(draftPath, draftJSON, 0o644); err != nil {
		fail("write draft: %v", err)
	}
	reviewPath := filepath.Join(*outDir, "review-"+date+".md")
	writeReview(reviewPath, lines, drafted, draftPath)

	fmt.Fprintf(os.Stderr, "\n[draft] draft bundle → %s\n[draft] REVIEW THIS  → %s\n", draftPath, reviewPath)
	fmt.Fprintf(os.Stderr, "\nYour review flow:\n")
	fmt.Fprintf(os.Stderr, "  1. Open %s — scrutinise the ⚠ flagged lines + the [抽查] spot-checks.\n", reviewPath)
	fmt.Fprintf(os.Stderr, "  2. In %s: for each flagged line, verify vs its source, then delete its\n", draftPath)
	fmt.Fprintf(os.Stderr, "     \"review\" field (= approved) — or fix/delete the whole line.\n")
	fmt.Fprintf(os.Stderr, "  3. bash build.sh %s   (refuses to sign while any \"review\" marker remains)\n", draftPath)
}

// ---- HN ----

type hnStory struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	By    string `json:"by"`
	Score int    `json:"score"`
	Time  int64  `json:"time"`
	Type  string `json:"type"`
}

func crawlHN(scan, minScore int) ([]candidate, error) {
	ids, err := getJSON[[]int64](hnBest)
	if err != nil {
		return nil, err
	}
	if scan < len(ids) {
		ids = ids[:scan]
	}
	var (
		mu  sync.Mutex
		out []candidate
		wg  sync.WaitGroup
		sem = make(chan struct{}, 8)
	)
	for _, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(id int64) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := getJSON[hnStory](fmt.Sprintf(hnItem, id))
			if err != nil || s.Type != "story" || s.URL == "" || s.Score < minScore {
				return
			}
			if !titleMatches(s.Title) || hostSkipped(s.URL) {
				return
			}
			mu.Lock()
			out = append(out, candidate{Title: s.Title, URL: s.URL, Source: "Hacker News", When: time.Unix(s.Time, 0).UTC(), Score: s.Score})
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out, nil
}

// ---- RSS / Atom ----

type rssDoc struct {
	Items []struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		PubDate     string `xml:"pubDate"`
		Date        string `xml:"date"`
	} `xml:"channel>item"`
	Entries []struct {
		Title string `xml:"title"`
		Links []struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
		} `xml:"link"`
		Summary   string `xml:"summary"`
		Content   string `xml:"content"`
		Updated   string `xml:"updated"`
		Published string `xml:"published"`
	} `xml:"entry"`
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

func cleanSummary(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

var dateLayouts = []string{time.RFC1123Z, time.RFC1123, time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02"}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func crawlRSS(feedURL, label string, aiOnly bool) ([]candidate, error) {
	raw, err := getBytes(feedURL)
	if err != nil {
		return nil, err
	}
	var doc rssDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -recencyDays)
	var out []candidate
	push := func(title, link, summary string, when time.Time) {
		title, link = strings.TrimSpace(title), strings.TrimSpace(link)
		if title == "" || link == "" || hostSkipped(link) {
			return
		}
		if !when.IsZero() && when.Before(cutoff) {
			return
		}
		if !aiOnly && !titleMatches(title) {
			return
		}
		out = append(out, candidate{Title: title, URL: link, Source: label, Summary: cleanSummary(summary), When: when})
	}
	for _, it := range doc.Items {
		when := parseDate(it.PubDate)
		if when.IsZero() {
			when = parseDate(it.Date)
		}
		push(it.Title, it.Link, it.Description, when)
	}
	for _, e := range doc.Entries {
		link := ""
		for _, l := range e.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				break
			}
		}
		if link == "" && len(e.Links) > 0 {
			link = e.Links[0].Href
		}
		when := parseDate(e.Published)
		if when.IsZero() {
			when = parseDate(e.Updated)
		}
		sum := e.Summary
		if sum == "" {
			sum = e.Content
		}
		push(e.Title, link, sum, when)
	}
	return out, nil
}

// ---- merge / rank ----

func dedupeAndRank(in []candidate) []candidate {
	// 1) exact URL dedup
	seen := map[string]bool{}
	var uniq []candidate
	for _, c := range in {
		k := normURL(c.URL)
		if seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, c)
	}
	// 2) rank by score then recency
	sort.Slice(uniq, func(i, j int) bool {
		if uniq[i].Score != uniq[j].Score {
			return uniq[i].Score > uniq[j].Score
		}
		return uniq[i].When.After(uniq[j].When)
	})
	// 3) near-duplicate TITLE pass: two outlets covering the same event have
	//    different URLs but overlapping titles. Keep the highest-ranked of each
	//    cluster so the same story doesn't reach the draft twice.
	var out []candidate
	var keptTokens []map[string]bool
	for _, c := range uniq {
		tok := titleTokens(c.Title)
		dup := false
		for _, kt := range keptTokens {
			if titleSimilar(tok, kt) {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		out = append(out, c)
		keptTokens = append(keptTokens, tok)
		if len(out) >= maxCands {
			break
		}
	}
	return out
}

// titleStop are low-signal words ignored when comparing titles.
var titleStop = map[string]bool{
	"the": true, "a": true, "an": true, "to": true, "of": true, "for": true, "and": true,
	"in": true, "on": true, "with": true, "is": true, "are": true, "as": true, "at": true,
	"by": true, "its": true, "new": true, "now": true, "ai": true, "how": true, "why": true,
	"what": true, "from": true, "over": true, "into": true, "that": true,
}

// titleTokens reduces a title to its set of significant lowercased word tokens.
func titleTokens(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len(w) < 3 || titleStop[w] {
			continue
		}
		m[w] = true
	}
	return m
}

// titleSimilar reports whether two token sets look like the same story (Jaccard
// ≥ 0.6). Requires ≥4 significant tokens on each side so short/CJK titles (which
// don't word-split) never trigger a false merge.
func titleSimilar(a, b map[string]bool) bool {
	if len(a) < 4 || len(b) < 4 {
		return false
	}
	inter := 0
	for w := range a {
		if b[w] {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return false
	}
	return float64(inter)/float64(union) >= 0.6
}

func normURL(u string) string {
	p, err := url.Parse(strings.ToLower(strings.TrimSpace(u)))
	if err != nil {
		return u
	}
	return p.Host + strings.TrimRight(p.Path, "/")
}

func titleMatches(title string) bool {
	t := strings.ToLower(title)
	for _, k := range keywords {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func hostSkipped(link string) bool {
	l := strings.ToLower(link)
	for _, h := range skipHosts {
		if strings.Contains(l, "://"+h) || strings.Contains(l, "://www."+h) {
			return true
		}
	}
	return false
}

// ---- draft + risk flagging ----

// draftItem is the model's per-line output: the feed fields plus review metadata.
type draftItem struct {
	feed.Line
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence"`
}

// buildEditorPrompt assembles the provider-agnostic system + user prompt and the
// srcText map (URL -> lowercased title+summary) that the grounding check uses.
func buildEditorPrompt(cands []candidate, n int) (system, user string, srcText map[string]string) {
	srcText = map[string]string{}
	var sb strings.Builder
	for i, c := range cands {
		if i >= maxPromptCands {
			break
		}
		srcText[c.URL] = strings.ToLower(c.Title + " " + c.Summary)
		when := ""
		if !c.When.IsZero() {
			when = c.When.Format("2006-01-02")
		}
		fmt.Fprintf(&sb, "%d. title=%q\n   src=%s date=%s score=%d url=%s\n", i+1, c.Title, c.Source, when, c.Score, c.URL)
		if c.Summary != "" {
			fmt.Fprintf(&sb, "   summary=%q\n", c.Summary)
		}
	}
	system = `You are the editor of "Cli Times", a one-line AI-news ticker for developers.
From the candidate stories, choose the ` + fmt.Sprint(n) + ` most valuable and write ONE line each.
EDITORIAL PRIORITY (rank highest first): 1) major/high-impact AI news; 2) OFFICIAL product &
feature-release info for developer tools (Claude/Claude Code, Codex, Gemini CLI, Cursor…), prefer
the vendor's own announcement; 3) notable tooling, papers, community. Prefer official/major sources.
STRICT RULES (legal + product):
- Write your OWN words; do NOT copy the headline verbatim. Convey the fact, not the phrasing.
- "t" <= 85 chars, factual, neutral, declarative. No clickbait/imperative.
- NEVER invent facts, numbers, or quotes. Only state a number if it appears in the candidate's
  title or summary. If an important story's number is NOT in the summary, KEEP the story but write
  it WITHOUT the number — a true number-free sentence beats dropping a major story.
- AVOID near-duplicates: if two candidates cover the SAME underlying event, include it only once.
- If a story is ABOUT prompt injection/jailbreaks, describe it without reproducing any injection string.
- ATTRIBUTION: set "src" to the ORIGINAL publication (derive it from the url's domain — e.g.
  arcprize.org -> "ARC Prize", theregister.com -> "The Register", openai.com -> "OpenAI"),
  NEVER the discovery channel such as "Hacker News". Use the candidate's url as-is; if it is an
  aggregator / re-syndication link rather than the original article, skip that story.
For EACH item also return:
- "evidence": the exact fact from the candidate's title/summary that supports your line (quote it).
- "confidence": "high" | "medium" | "low" (how sure you are the line is accurate & well-sourced).
Return ONLY a JSON object (no prose, no code fence) of exactly this shape:
{"items":[{"t":"...","src":"...","url":"https://...","lang":"en","topic":"llm|agents|tooling|research|policy|funding|other","evidence":"...","confidence":"high"}]}
Return ` + fmt.Sprint(n) + ` items ranked best-first; there are enough candidates, so fill all ` + fmt.Sprint(n) + ` unless truly fewer are worthy.`
	user = "Candidates:\n" + sb.String()
	return system, user, srcText
}

// parseAndFlag turns a model's raw JSON reply into validated feed lines. It is
// provider-agnostic and treats the model as UNTRUSTED: every line runs the same
// sanitize → width cap → urlSafe gauntlet (a failing line is dropped, not
// trusted), then gets its deterministic risk flag. extractJSONArray tolerates
// either a bare array or an {"items":[...]} wrapper (OpenAI JSON-object mode).
func parseAndFlag(raw string, srcText map[string]string) ([]feed.Line, []draftItem, error) {
	var parsed []draftItem
	if err := json.Unmarshal([]byte(extractJSONArray(raw)), &parsed); err != nil {
		return nil, nil, fmt.Errorf("model did not return valid JSON: %w\nraw: %s", err, raw)
	}
	var lines []feed.Line
	var kept []draftItem
	for _, d := range parsed {
		clean, _, good := sanitize.Clean(d.Text, 0)
		if !good || clean == "" || uniseg.StringWidth(clean) > maxHeadline || urlSafe(d.URL) == "" {
			continue
		}
		d.Text = clean
		if d.Lang == "" {
			d.Lang = "en"
		}
		d.Source = attributeSource(d.Source, d.URL)
		d.Line.Review = flagReason(d.Text, srcText[d.URL], d.Confidence, d.Sponsored)
		lines = append(lines, d.Line)
		kept = append(kept, d)
	}
	return lines, kept, nil
}

// numRE captures a "claim number": a token that STARTS with a digit (optionally
// a $ prefix) and may carry a %/magnitude suffix — e.g. 89%, $150M, 30, 2.2. The
// leading boundary [^\p{L}\d-] deliberately EXCLUDES a preceding letter or hyphen,
// so digits that are part of an identifier (ARC-AGI-1, GPT-5, v4, Claude 3.5) are
// NOT treated as statistics. Fabricated stats begin with a digit; version/product
// names carry an alphabetic stem, so this split is what separates them.
var numRE = regexp.MustCompile(`(?:^|[^\p{L}\d-])(\$?\d[\d.,]*(?:%|[kmbt])?)`)

// numCore reduces a captured token to its comparable digit core: drop the $,
// a trailing %/magnitude letter, and thousands commas.
func numCore(tok string) string {
	tok = strings.TrimPrefix(tok, "$")
	tok = strings.TrimRight(tok, "%kmbt")
	tok = strings.ReplaceAll(tok, ",", "")
	return strings.Trim(tok, ".")
}

// numbersSupported reports whether every claim number in text also appears in
// sourceText. A fabricated stat is the highest-risk hallucination, so a number
// the source can't back is the strongest signal to force human review.
func numbersSupported(text, sourceText string) bool {
	ls := strings.ToLower(sourceText)
	for _, m := range numRE.FindAllStringSubmatch(strings.ToLower(text), -1) {
		core := numCore(m[1])
		if core == "" {
			continue
		}
		if !strings.Contains(ls, core) {
			return false
		}
	}
	return true
}

// flagReason returns a non-empty human-readable reason if the line needs review
// (in auto mode it is then dropped), or "" if it is auto-cleared. Order: worst
// risk first. Note: Anthropic/Claude are NO LONGER flagged — vendor news about
// the platform is a wanted editorial category (Howie's call), so it ships like
// any other. The number check stays as the hard guard for the "no unverified
// numbers" rule; confidence only drops "low" (medium is kept, to avoid a thin feed).
func flagReason(text, sourceText, confidence string, sponsored bool) string {
	if !numbersSupported(text, sourceText) {
		return "數字未在來源出現,需查證原文"
	}
	if sponsored {
		return "贊助內容,需確認揭露與說法"
	}
	if strings.ToLower(strings.TrimSpace(confidence)) == "low" {
		return "模型自評信心=low"
	}
	return ""
}

// publisherByHost maps known hosts to a clean publisher label. Suffix-matched, so
// blog.cloudflare.com resolves via cloudflare.com.
var publisherByHost = map[string]string{
	"arcprize.org":      "ARC Prize",
	"theregister.com":   "The Register",
	"arstechnica.com":   "Ars Technica",
	"openai.com":        "OpenAI",
	"anthropic.com":     "Anthropic",
	"deepmind.google":   "DeepMind",
	"huggingface.co":    "Hugging Face",
	"simonwillison.net": "Simon Willison",
	"technews.tw":       "科技新報",
	"fb.com":            "Meta",
	"databricks.com":    "Databricks",
	"cloudflare.com":    "Cloudflare",
	"github.com":        "GitHub",
	"fastmail.com":      "Fastmail",
}

// attributeSource fixes attribution deterministically: if the model set the source
// to a DISCOVERY channel (Hacker News / Reddit / Lobsters) or left it blank, derive
// the real publisher from the article URL's host instead. Otherwise trust the model
// (it often gives a nicer byline than a bare domain).
func attributeSource(modelSrc, rawurl string) string {
	s := strings.ToLower(strings.TrimSpace(modelSrc))
	discovery := s == "" || s == "hn" || strings.Contains(s, "hacker news") ||
		strings.Contains(s, "hackernews") || strings.Contains(s, "reddit") ||
		strings.Contains(s, "lobste")
	if !discovery {
		return modelSrc
	}
	if name := publisherFromURL(rawurl); name != "" {
		return name
	}
	return modelSrc
}

func publisherFromURL(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	host = strings.TrimPrefix(host, "www.")
	for k, v := range publisherByHost {
		if host == k || strings.HasSuffix(host, "."+k) {
			return v
		}
	}
	// Generic fallback: the registrable label, Title-cased. Better than a discovery
	// channel even if imperfect (the full URL is always shown in the brief anyway).
	parts := strings.Split(host, ".")
	if len(parts) >= 2 {
		if name := parts[len(parts)-2]; name != "" {
			return strings.ToUpper(name[:1]) + name[1:]
		}
	}
	return ""
}

func extractJSONArray(s string) string {
	start := strings.IndexByte(s, '[')
	end := strings.LastIndexByte(s, ']')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func urlSafe(u string) string {
	if len(u) == 0 || len(u) > 300 {
		return ""
	}
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return ""
	}
	for _, r := range u {
		if r < 0x21 || r > 0x7E {
			return ""
		}
	}
	return u
}

// ---- output files ----

func writeCandidates(path string, cands []candidate) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Cli Times — candidates (%s)\n\n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	for i, c := range cands {
		if i >= maxPromptCands {
			break
		}
		d := ""
		if !c.When.IsZero() {
			d = c.When.Format("2006-01-02")
		}
		fmt.Fprintf(&sb, "- [%s, score %d, %s] %s\n  %s\n", c.Source, c.Score, d, c.Title, c.URL)
	}
	_ = os.WriteFile(path, []byte(sb.String()), 0o644)
}

func writeReview(path string, lines []feed.Line, drafted []draftItem, draftPath string) {
	// index the model metadata by line text for display
	meta := map[string]draftItem{}
	for _, d := range drafted {
		meta[d.Text] = d
	}
	var flagged, cleared []int
	for i, ln := range lines {
		if ln.Review != "" {
			flagged = append(flagged, i)
		} else {
			cleared = append(cleared, i)
		}
	}
	// random spot-check sample among cleared
	spot := map[int]bool{}
	if len(cleared) > 0 {
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		idx := append([]int(nil), cleared...)
		r.Shuffle(len(idx), func(a, b int) { idx[a], idx[b] = idx[b], idx[a] })
		for i := 0; i < spotCheck && i < len(idx); i++ {
			spot[idx[i]] = true
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Cli Times — 審核 (%s)\n\n", time.Now().UTC().Format("2006-01-02"))
	fmt.Fprintf(&sb, "共 %d 條:⚠ %d 條需審核、✓ %d 條自動通過(其中 %d 條標記抽查)。\n\n", len(lines), len(flagged), len(cleared), len(spot))
	fmt.Fprintf(&sb, "## 你的流程\n")
	fmt.Fprintf(&sb, "1. 看下面 ⚠ 每一條:點來源連結,確認說法/數字對得上。\n")
	fmt.Fprintf(&sb, "2. 到 `%s`:確認 OK 的,**刪掉那一行的 \"review\" 欄位**(= 我審過了);錯的就改文字或整條刪掉。\n", filepath.Base(draftPath))
	fmt.Fprintf(&sb, "3. 順手抽查下面標 [抽查] 的幾條。\n")
	fmt.Fprintf(&sb, "4. `bash build.sh %s` — 只要還有任何 \"review\" 標記,它就拒簽。\n\n", draftPath)

	line := func(i int, tag string) {
		ln := lines[i]
		d := meta[ln.Text]
		fmt.Fprintf(&sb, "%d. %s%s\n", i+1, tag, ln.Text)
		if ln.Review != "" {
			fmt.Fprintf(&sb, "   ⚠ 原因:%s\n", ln.Review)
		}
		fmt.Fprintf(&sb, "   來源 %s · 信心 %s · %s\n", ln.Source, orDash(d.Confidence), ln.URL)
		if d.Evidence != "" {
			fmt.Fprintf(&sb, "   佐證(模型引用):%s\n", d.Evidence)
		}
		fmt.Fprintln(&sb)
	}

	fmt.Fprintf(&sb, "## ⚠ 需審核 (%d)\n\n", len(flagged))
	if len(flagged) == 0 {
		fmt.Fprintf(&sb, "(這批沒有被標記的行 —— 但仍請抽查下面幾條。)\n\n")
	}
	for _, i := range flagged {
		line(i, "")
	}
	fmt.Fprintf(&sb, "## ✓ 自動通過 (%d)\n\n", len(cleared))
	for _, i := range cleared {
		tag := ""
		if spot[i] {
			tag = "[抽查] "
		}
		line(i, tag)
	}
	_ = os.WriteFile(path, []byte(sb.String()), 0o644)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---- http ----

func getJSON[T any](u string) (T, error) {
	var zero, out T
	body, err := getBytes(u)
	if err != nil {
		return zero, err
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, err
	}
	return out, nil
}

func getBytes(u string) ([]byte, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("User-Agent", "cli-times-curate/0.1")
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "curate: "+format+"\n", a...)
	os.Exit(1)
}
