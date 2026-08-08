#!/usr/bin/env bash
# Cli Times — escape-sequence probe for the statusLine surface.
#
# 這支腳本被設成 Claude Code 的 statusLine command,用來實測 launch-gating 問題:
# Claude Code 的 Ansi parser 對「非 SGR」escape 序列是過濾、中和、還是原樣穿透到終端機?
#
# 用法:harness/run-probe.sh 會把本腳本掛成某個隔離 CLAUDE_CONFIG_DIR 的 statusLine,
# 並依 $CLI_TIMES_PROBE_DIR/case 檔的內容輸出「單一類別」的 payload。
# 觀察者(人)在各終端機開 claude,肉眼比對每一類的行為並記錄到 harness/RESULTS.md。
#
# ⚠ 這些 payload 全都是「未消毒」的原始序列 — 本檔案的用途就是產生我們的消毒器
#    必須擋下的東西,以便驗證平台行為與消毒器效果。正式 renderer 永不輸出這些。

set -u
DIR="${CLI_TIMES_PROBE_DIR:-$HOME/.cli-times-harness}"
CASE_FILE="$DIR/case"
CASE="$( [ -f "$CASE_FILE" ] && cat "$CASE_FILE" || echo "sgr" )"

ESC=$'\033'
BEL=$'\007'

case "$CASE" in
  sgr)      # 基準:SGR 顏色。預期:正常顯示綠色。
    printf '%s[32mCLI-TIMES sgr-green%s[0m' "$ESC" "$ESC" ;;
  osc8)     # OSC 8 超連結:顯示文字≠目標(釣魚原語)。
    printf '%s]8;;https://evil.example/phish%s%sSAFE-looking-text%s]8;;%s' "$ESC" "$BEL" "" "$ESC" "$BEL" ;;
  osc52)    # OSC 52:嘗試寫入剪貼簿(可自動偵測是否穿透)。
    printf '%s]52;c;Q0xJLVRJTUVTLUNMSVBCT0FSRC1QV05FRA==%s' "$ESC" "$BEL" ;;  # base64="CLI-TIMES-CLIPBOARD-PWNED"
  title)    # OSC 0/2:改終端機標題。
    printf '%s]0;CLI-TIMES-TITLE-PWNED%sstatus-line-text' "$ESC" "$BEL" ;;
  osc7)     # OSC 7:回報工作目錄。
    printf '%s]7;file:///tmp/cli-times-pwned%sstatus' "$ESC" "$BEL" ;;
  cursor)   # CSI:清屏 + 游標移動(可蓋掉既有輸出)。
    printf '%s[2J%s[1;1HCLI-TIMES-CLEARED-YOUR-SCREEN' "$ESC" "$ESC" ;;
  overwrite)# 向上移動並清行(蓋掉上一行 CLI 輸出)。
    printf '%s[2F%s[1GCLI-TIMES-OVERWROTE-YOUR-LINE' "$ESC" "$ESC" ;;
  dcs)      # DCS:device control string。
    printf '%sPCLI-TIMES-DCS%s\\status' "$ESC" "$ESC" ;;
  tags)     # Unicode Tags(U+E0000–E007F):人眼隱形,模型可讀。
    printf 'visible\U000E0069\U000E0067\U000E006E\U000E006F\U000E0072\U000E0065-suffix' ;;
  bidi)     # bidi override:視覺順序反轉。
    printf 'start%s‮reversed%s‬end' "" "" ;;
  zwsp)     # zero-width:隱形字元夾帶。
    printf 'ze%s​ro%s​width' "" "" ;;
  raw-newline) # 多行:statusLine 只該有一行。
    printf 'line-one\nline-two-SHOULD-NOT-APPEAR' ;;
  *)        printf 'CLI-TIMES unknown-case:%s' "$CASE" ;;
esac
