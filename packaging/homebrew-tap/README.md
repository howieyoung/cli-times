# howieyoung/homebrew-tap

Homebrew tap for [Cli Times](https://github.com/howieyoung/cli-times) — a curated
one-line AI-news ticker for the Claude Code status line.

## Install

```bash
brew install howieyoung/tap/cli-times
brew services start cli-times     # ~6-hourly pull-only feed refresh
```

Then add to `~/.claude/settings.json` (top level) and reopen Claude Code:

```json
"statusLine": { "type": "command", "command": "cli-times", "refreshInterval": 1 }
```

Full brief any time: `cli-times today`

## What this installs

Builds **from source** on your machine (no unsigned prebuilt binary, no macOS
Gatekeeper prompt):

- `cli-times` — the renderer: zero network, reads one local cache file, prints one
  line. Never touches your Claude/Codex account, code, or prompts.
- `cli-times-update` — the only networked component: one pull-only HTTPS GET every
  ~6h to fetch the day's Ed25519-signed brief. No telemetry, no identifiers.

Content is signature-verified before parse and fail-closed (a bad/expired feed
shows nothing rather than anything suspicious).

Experimental project, separate from Protico. Feedback → howie@protico.io
