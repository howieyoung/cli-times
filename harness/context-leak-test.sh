#!/usr/bin/env bash
# Launch-gating test #1(自動化嘗試):statusLine 內容是否落地到 transcript /
# telemetry / debug log —— 也就是模型或 Anthropic 是否可讀到我們印的內容。
#
# 做法:隔離的 CLAUDE_CONFIG_DIR,statusLine 輸出唯一 canary,跑一次 claude,
# 然後在整個 config dir 內搜尋 canary。命中 = 內容確實落地 → §1 的宣稱需下修。
#
# ⚠ 會消耗少量 API token。若 statusLine 在 -p 非互動模式不執行,結果為 inconclusive,
#   需改用互動 session(見 RESULTS.md 的人工步驟)。
set -u
CANARY="CLITIMES_CANARY_$(date +%s)_$$"
CFG="$(mktemp -d)/cfg"; mkdir -p "$CFG"
PROBE="$CFG/probe.sh"

cat > "$PROBE" <<EOF
#!/usr/bin/env bash
printf '%s' "$CANARY"
EOF
chmod +x "$PROBE"

cat > "$CFG/settings.json" <<EOF
{ "statusLine": { "type": "command", "command": "$PROBE" } }
EOF

echo "canary: $CANARY"
echo "config: $CFG"
echo "跑 claude(30s 逾時,print 模式)..."
CLAUDE_CONFIG_DIR="$CFG" timeout 30 claude -p "reply with the single word: ok" >/dev/null 2>&1
echo "claude exit: $?"

echo
echo "=== 在 config dir 搜尋 canary(命中檔案代表內容落地) ==="
HITS="$(grep -rl "$CANARY" "$CFG" 2>/dev/null | grep -v "probe.sh\|settings.json")"
if [ -n "$HITS" ]; then
  echo "⚠ 命中 — statusLine 內容落地到:"
  echo "$HITS" | sed 's/^/    /'
  echo "→ 結論:§1 的『不進 model 可讀處』宣稱需下修,並在文案中誠實揭露此路徑。"
else
  echo "✓ 未在 transcript/telemetry 找到 canary。"
  echo "  注意:若 statusLine 在 -p 模式未執行,此為 inconclusive → 需互動 session 複驗。"
  echo "  檢查 statusLine 是否曾執行(debug):"
  CLAUDE_CONFIG_DIR="$CFG" timeout 20 claude --debug -p "ok" 2>&1 | grep -i "status" | head -5 || echo "    (debug 無 status 相關輸出)"
fi
echo "DONE(手動清理:rm -rf $(dirname "$CFG"))"
