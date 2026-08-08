# Homebrew formula for Cli Times — builds FROM SOURCE on the user's machine (no
# unsigned prebuilt binary → no macOS Gatekeeper prompt, and the user compiles the
# exact public source). The signing PUBLIC key + feed URL are embedded at build
# time via ldflags; they are public by design (only the private key stays secret).
#
# TO PUBLISH: create the public repo github.com/howieyoung/cli-times, tag v0.1.0,
# then uncomment + fill url/sha256/version below (sha256 = `shasum -a 256` of the
# release tarball), and host THIS file in a tap repo `howieyoung/homebrew-tap` so
# `brew install howieyoung/tap/cli-times` resolves. (`head` install works as soon
# as the repo is public, even before a tagged release.)
class CliTimes < Formula
  desc "Curated one-line AI-news ticker for the Claude Code status line"
  homepage "https://github.com/howieyoung/cli-times"
  # Pinned release (v0.1.0):
  url "https://github.com/howieyoung/cli-times/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "c68cd464eab27196f1363d26abcbf85b54a6d2e9a63cbf676103e2c0d41d1bf0"
  license "MIT"
  head "https://github.com/howieyoung/cli-times.git", branch: "main"

  depends_on "go" => :build

  # Public signing key (safe to embed) + key id + the pinned feed URL. Regenerate
  # the formula if the signing key ever rotates.
  PUBKEY = "Vm3nTumkZfWs87OwNiJNqEJJSINq2hSfxEB4kbTCYXU=".freeze
  KID = "k1".freeze
  FEED_URL = "https://cli-times-feed.pages.dev/feed.json".freeze

  def install
    base = %W[
      -s -w
      -X main.pinnedKeyB64=#{PUBKEY}
      -X main.pinnedKeyID=#{KID}
    ]
    # renderer: the zero-network status-line printer (reads one cache file).
    render_ldflags = base + ["-X main.version=#{version}"]
    system "go", "build",
           *std_go_args(ldflags: render_ldflags.join(" "), output: bin/"cli-times"),
           "./cmd/renderer"
    # updater: the ONLY networked component; feed URL pinned so it needs no config.
    update_ldflags = base + ["-X main.feedURL=#{FEED_URL}"]
    system "go", "build",
           *std_go_args(ldflags: update_ldflags.join(" "), output: bin/"cli-times-update"),
           "./cmd/updater"
  end

  # Schedule the ~6h pull-only refresh. `brew services start cli-times` loads it.
  service do
    run [opt_bin/"cli-times-update"]
    run_type :interval
    interval 21600
  end

  def post_install
    # Best-effort first fetch so the ticker isn't blank before the timer's first
    # run. The updater is fail-closed (always exits 0), so this never breaks install.
    system bin/"cli-times-update"
  end

  def caveats
    <<~EOS
      1) Start the ~6-hourly updater (one pull-only HTTPS GET each time, no telemetry):
           brew services start cli-times

      2) Show the ticker in Claude Code — add to ~/.claude/settings.json (top level):
           "statusLine": { "type": "command", "command": "cli-times", "refreshInterval": 1 }
         then reopen Claude Code.

      Full brief with links:  cli-times today

      If the status line is blank, the signed feed hasn't landed yet — run
      `cli-times today` to check, or wait for the updater's next run.
    EOS
  end

  test do
    assert_match "cli-times", shell_output("#{bin}/cli-times --version")
    # No feed cache in the test sandbox → fail-closed → prints nothing, exits 0.
    assert_equal "", shell_output("#{bin}/cli-times")
    # The updater is present and fail-closed even with no network (exits 0).
    system bin/"cli-times-update"
  end
end
