// Command updater is the ONLY networked component. It runs on a timer (launchd /
// systemd, ~6h), fetches the signed feed bundle from the CDN, verifies it, and
// atomically replaces the local cache the renderer reads. It is split from the
// renderer on purpose: the renderer has no network at all.
//
// Security posture:
//   - Hard download size cap, then Ed25519 verify-BEFORE-parse with the PINNED
//     key, expiry check, and a monotonic high-water-mark (rollback/replay defense)
//     kept in a separate 0600 file — never trust an older-but-valid bundle.
//   - Atomic write: temp in the same 0700 dir → fsync → rename; refuse if the
//     cache dir isn't a user-owned regular directory.
//   - NEVER writes ~/.claude/settings.json or anything but the one cache file +
//     the high-water-mark file. No shell, no exec.
//   - Fail-closed by omission: on ANY error it leaves the existing cache intact
//     (the renderer keeps showing it until it expires, then blanks) and exits 0
//     so the timer never spirals. Errors go to a local log only.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/howieyoung/cli-times/internal/feed"
)

// Pinned at build time via -ldflags (same scheme as the renderer). feedURL is the
// CDN endpoint serving the signed envelope; set it when you provision hosting:
//
//	-X main.feedURL=https://feed.clitimes.dev/feed.json
//
// pinnedKeyB64/pinnedKeyID must match the signing key. Env vars override for dev.
var (
	feedURL      string
	pinnedKeyB64 string
	pinnedKeyID  string
)

const maxDownload = 64 * 1024

func main() {
	// Never let the timer see a crash.
	defer func() { _ = recover() }()
	if err := run(); err != nil {
		logErr(err)
		// Intentionally exit 0: a failed poll is not a fatal condition.
	}
}

func run() error {
	url := feedURL
	if v := os.Getenv("CLI_TIMES_FEED_URL"); v != "" {
		url = v
	}
	if url == "" {
		return fmt.Errorf("no feed URL configured (build with -X main.feedURL=... or set CLI_TIMES_FEED_URL)")
	}
	pub, kid, err := loadPinnedKey()
	if err != nil {
		return err
	}
	dir, err := cacheDir()
	if err != nil {
		return err
	}

	raw, err := fetch(url)
	if err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	hwmPath := filepath.Join(dir, "hwm")
	lastSeen := readHWM(hwmPath) // -1 if none

	b, err := feed.Verify(raw, map[string]ed25519.PublicKey{kid: pub}, lastSeen, time.Now())
	if err != nil {
		// ErrRollback here means the CDN served an older-or-equal version than we
		// already have — a benign no-op (or a replay attempt); keep the cache.
		return fmt.Errorf("verify: %w", err)
	}

	// Verified and newer. Atomically replace the cache, then advance the high-water mark.
	if err := atomicWrite(filepath.Join(dir, "cache.json"), raw); err != nil {
		return fmt.Errorf("write cache: %w", err)
	}
	if err := atomicWrite(hwmPath, []byte(strconv.Itoa(b.Version))); err != nil {
		return fmt.Errorf("write hwm: %w", err)
	}
	logInfo(fmt.Sprintf("updated to v%d (%d lines), expires %s", b.Version, len(b.Lines), b.Expires.Format(time.RFC3339)))
	return nil
}

func fetch(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "cli-times-updater/0.1")
	c := &http.Client{Timeout: 20 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	// Read at most maxDownload+1 so an oversized body is caught, not silently truncated.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDownload {
		return nil, fmt.Errorf("feed exceeds %d-byte cap", maxDownload)
	}
	return raw, nil
}

func loadPinnedKey() (ed25519.PublicKey, string, error) {
	b64, kid := pinnedKeyB64, pinnedKeyID
	if b64 == "" {
		b64 = os.Getenv("CLI_TIMES_PUBKEY")
		kid = os.Getenv("CLI_TIMES_KID")
	}
	if b64 == "" || kid == "" {
		return nil, "", fmt.Errorf("no pinned public key")
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, "", fmt.Errorf("bad pinned public key")
	}
	return ed25519.PublicKey(raw), kid, nil
}

// cacheDir returns (and creates, 0700) the cache directory, verifying it is a
// user-owned regular directory — not a symlink someone pre-planted.
func cacheDir() (string, error) {
	var base string
	if d := os.Getenv("CLI_TIMES_DIR"); d != "" {
		base = d
	} else if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		base = filepath.Join(x, "cli-times")
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache", "cli-times")
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	fi, err := os.Lstat(base)
	if err != nil {
		return "", err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", fmt.Errorf("cache dir %q is not a real directory", base)
	}
	// Refuse a dir owned by another user (e.g. a hostile pre-planted path via
	// XDG_CACHE_HOME on a shared host). Content is Ed25519-verified regardless, so
	// this only hardens against denial/downgrade — but it matches the stated
	// guarantee. Skipped where ownership is unavailable (non-unix).
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("cache dir %q is not owned by the current user", base)
	}
	return base, nil
}

// atomicWrite writes data to path via same-dir temp → fsync → rename (0600).
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeded
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func readHWM(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1
	}
	return n
}

func logPath() string {
	d, err := cacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "updater.log")
}

func logErr(err error)   { appendLog("ERROR " + err.Error()) }
func logInfo(msg string) { appendLog("INFO  " + msg) }
func appendLog(line string) {
	p := logPath()
	if p == "" {
		return
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}
