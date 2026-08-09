# CLI Times

**A curated one-line AI-news ticker that lives in your AI coding CLI's wait state.**

While Claude Code (and other AI CLIs) sit idle waiting for a response, your status
line quietly cycles a hand-curated headline of what's new in AI — models, agent
tooling, research, and official product releases developers should know about.

```
 ⠋ CT: New open-weight model tops the latest agentic-coding benchmark   · official blog
```

Type `cli-times today` any time to expand the full brief with source links.

- **Free.** No account, no telemetry, no API tokens consumed.
- **Safe by construction.** The status-line renderer has *zero* network code and
  cannot run commands — it reads one local, cryptographically-signed file and
  prints one line. Bad or expired content shows *nothing* rather than anything
  suspicious (fail-closed).
- **Opt-in and reversible.** You paste one line into your own settings; uninstall
  is three commands.

> Experimental project, independent of any vendor. Not affiliated with or endorsed
> by Anthropic.

---

## Install (macOS / Linux)

Built **from source** via Homebrew — no unsigned prebuilt binary, no macOS
Gatekeeper prompt, and you compile the exact public source. **All three steps are
required to see the ticker** — installing alone won't change your status line,
because Claude Code only runs a status-line command you've opted into (step 3).

**1. Install** (builds the renderer + updater; fetches the first brief):

```bash
brew install howieyoung/tap/cli-times
```

**2. Start the auto-updater** (one pull-only HTTPS request every ~6h):

```bash
brew services start cli-times
```

**3. Turn on the status line** — add this to `~/.claude/settings.json` (top-level
object), then reopen Claude Code. *(This step is what actually makes the ticker
appear; `brew install` also prints this reminder.)*

```json
"statusLine": { "type": "command", "command": "cli-times", "refreshInterval": 1 }
```

Done — the ticker cycles in your status line, and the leading braille mark animates
once per second while you wait.

**See the full brief any time** (works without step 3; source links are clickable
in modern terminals):

```bash
cli-times today
```

> If the status line is blank, the signed feed hasn't landed yet — run
> `cli-times today` to check, or wait for the updater's next run.
> Prefer no animation? Set `CLI_TIMES_NO_ANIM=1`, or drop `refreshInterval`.

---

## What you get

| | |
|---|---|
| **Curated, not scraped** | Headlines are AI-drafted from reputable/official sources, risk-checked, and signed before they ship. One self-written sentence + a link to the original source. |
| **Fresh** | A new signed brief is published every ~6 hours; your machine pulls it with one HTTPS request on the same cadence. |
| **Source-guaranteed** | Every ticker line ends with its publication; the expanded brief shows the full URL. |
| **Bilingual-ready** | Source and display language are user-selectable (English / 繁體中文). |
| **Costs you nothing** | The status line runs locally and consumes no API tokens. See [Privacy & cost](#privacy--cost). |

---

## How it works

Three small Go programs with a deliberately strict split of responsibilities:

```
cmd/renderer   The status-line program. ZERO network, no shell, no writes.
               Reads one local cache file → verifies the signature → prints one line.
cmd/updater    The ONLY networked component. Every ~6h: one pull-only HTTPS GET of
               the signed feed → verify → atomic replace of the local cache. Fail-closed.
cmd/sign       Offline signing tool. The private key never touches a server or CI*.
cmd/curate     The editorial pipeline: crawl approved sources → draft → risk-flag.
```

The feed is an **Ed25519-signed bundle**. The signature is verified *before* the
JSON is parsed, against a public key **pinned into the binary at build time**, under
a hard size cap. Text is run through a rejection-based sanitizer (Unicode-category
filtering + positive allowlist) so nothing can smuggle escape sequences or invisible
characters into your terminal. Anything that fails any check renders as blank.

The renderer is intentionally powerless: it never reads your prompts, code, or
files, never writes to `~/.claude/settings.json`, and never updates its own code.

<sub>*In automated publishing, the signing key lives only in a protected CI secret and
is wiped after each run.</sub>

---

## Privacy & cost

- **$0 to you.** Anthropic's own docs note a status-line command "runs locally and
  does not consume API tokens." The renderer has no network code at all — it reads
  one file and prints one line, so it never calls any AI API or touches your
  Claude / Codex account.
- **No telemetry.** The renderer sends nothing. The separate updater makes one
  pull-only HTTPS request to a CDN every ~6h to fetch the day's brief; the CDN sees
  your IP like any download, and we receive only aggregate request counts — no
  identifiers, cookies, or content are sent back.

---

## Uninstall

```bash
# 1) remove the statusLine block you added to ~/.claude/settings.json
brew services stop cli-times
brew uninstall cli-times
brew untap howieyoung/tap        # optional
rm -rf ~/.cache/cli-times        # cached feed + logs
```

---

## Build from source (development)

Requires Go (see `go.mod` for the version).

```bash
go build ./...
go test ./...                                                   # unit tests
go test ./internal/sanitize -run=xxx -fuzz=FuzzClean -fuzztime=30s   # sanitizer fuzz
```

Layout:

```
cmd/renderer      status-line renderer (zero-network)
cmd/updater       the one networked component (fetch + verify + atomic write)
cmd/sign          offline Ed25519 signing tool (keygen / bundle)
cmd/curate        editorial pipeline (crawl → draft → risk-flag)
internal/sanitize rejection-based text sanitizer + property fuzz
internal/feed     signed-bundle format + verify-before-parse
packaging/        Homebrew formula + launchd/systemd timer templates
```

---

## Content

Cli Times scans a broad range of reputable places across the web — official
vendor and research blogs, major technology outlets, and regional tech media —
and surfaces the handful of items that actually matter to developers. We don't
favor any single publisher: each cycle we pick the most relevant stories wherever
they appear, rewrite each in our own words, and link back to the original. Numbers
that can't be grounded in the source are dropped, not guessed.

**Building something in AI developers should know about** — a launch, a tool, a
paper, a community? Tell us: **howie@protico.io**.

---

## License

MIT — see [LICENSE](LICENSE).
