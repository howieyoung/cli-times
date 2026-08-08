#!/usr/bin/env bash
# 端到端 demo + fail-closed 驗證。用真正的 ESC 位元組測惡意內容被跳過。
set -u
export PATH="/opt/homebrew/bin:$PATH"
cd ~/cli-times
go build -o /tmp/clt-sign ./cmd/sign && go build -o /tmp/clt-render ./cmd/renderer || exit 1

WORK=/tmp/clt-demo; rm -rf "$WORK"; mkdir -p "$WORK/keys"
eval "$(/tmp/clt-sign keygen -out "$WORK/keys" -kid k1 2>/dev/null)"
export CLI_TIMES_PUBKEY CLI_TIMES_KID CLI_TIMES_DIR="$WORK"
echo "PUBKEY ${CLI_TIMES_PUBKEY:0:16}...  KID=$CLI_TIMES_KID"

# bundle.json 用 python 產生:一條含真 ESC 的惡意內容(清屏+OSC52)。
python3 - "$WORK/bundle.json" <<'PY'
import json, sys
ESC="\x1b"
lines=[
 {"t":"OpenAI ships GPT-5.5; devs report 30% faster tool calls","src":"Hacker News","lang":"en"},
 {"t":"Anthropic 發布 Claude Code 2.2,新增原生 statusLine 主題","src":"官方部落格","lang":"zh-TW"},
 {"t":"Vercel launches AI SDK v6 with agent primitives","src":"vercel.com","lang":"en","sponsored":True},
 {"t":f"EVIL {ESC}[2J{ESC}]52;c;QQ=={chr(7)} wiped your screen","src":"attacker","lang":"en"},
 {"t":"This is an extremely long headline that should be truncated cleanly at the column boundary without splitting any grapheme or leaving a dangling reset code anywhere","src":"test","lang":"en"},
]
json.dump({"v":42,"issued":"2026-08-08T00:00:00Z","expires":"2099-01-01T00:00:00Z","lines":lines},
          open(sys.argv[1],"w"), ensure_ascii=False)
PY

/tmp/clt-sign bundle -key "$WORK/keys/priv.key" -kid k1 -in "$WORK/bundle.json" -out "$WORK/cache.json"
echo "cache bytes: $(wc -c < "$WORK/cache.json")"

echo
echo "=== 1) 正常渲染:每個時間槽輪播一行(cat -v 揭露任何 escape 位元組) ==="
# 直接呼叫 render 內部的 slot 邏輯:用不同的假時間。renderer 依 wall-clock 選行,
# 這裡用 5 次呼叫並各自 sleep 讓 slot 前進(slotSeconds=8,故用 faketime 較準)。
# 簡化:直接印 5 次,觀察是否恆為單行、無 ESC 洩漏、惡意行被跳過。
for i in 1 2 3 4 5; do
  OUT="$(COLUMNS=100 /tmp/clt-render | cat -v)"
  printf '  [%d] %s\n' "$i" "$OUT"
done

echo
echo "=== 2) 惡意行單獨測:把 bundle 改成只剩惡意那一條 → 應完全空白 ==="
python3 - "$WORK/evil.json" <<'PY'
import json,sys
ESC="\x1b"
json.dump({"v":43,"issued":"2026-08-08T00:00:00Z","expires":"2099-01-01T00:00:00Z",
 "lines":[{"t":f"{ESC}[2Jpwned","src":"x","lang":"en"}]}, open(sys.argv[1],"w"))
PY
/tmp/clt-sign bundle -key "$WORK/keys/priv.key" -kid k1 -in "$WORK/evil.json" -out "$WORK/cache.json"
OUT="$(COLUMNS=100 /tmp/clt-render | cat -v)"
echo "  output=[$OUT]  (期望:空)"

echo
echo "=== 3) fail-closed:竄改 cache 一個位元組 → 驗章失敗 → 空白 ==="
python3 - "$WORK/cache.json" <<'PY'
import sys
p=sys.argv[1]; b=bytearray(open(p,'rb').read())
i=b.find(b'pwned'); b[i]=ord('X')  # 動 payload 內容
open(p,'wb').write(b)
PY
# 換回正常 bundle 但用竄改後的簽章
OUT="$(COLUMNS=100 /tmp/clt-render | cat -v)"
echo "  output=[$OUT]  (期望:空,因簽章不符)"

echo
echo "=== 4) fail-closed:過期 bundle → 空白 ==="
python3 - "$WORK/expired.json" <<'PY'
import json,sys
json.dump({"v":44,"issued":"2020-01-01T00:00:00Z","expires":"2020-01-02T00:00:00Z",
 "lines":[{"t":"stale","src":"x","lang":"en"}]}, open(sys.argv[1],"w"))
PY
/tmp/clt-sign bundle -key "$WORK/keys/priv.key" -kid k1 -in "$WORK/expired.json" -out "$WORK/cache.json"
OUT="$(COLUMNS=100 /tmp/clt-render | cat -v)"
echo "  output=[$OUT]  (期望:空,因已過期)"

echo
echo "=== 5) fail-closed:cache 不存在 → 空白 ==="
rm -f "$WORK/cache.json"
OUT="$(COLUMNS=100 /tmp/clt-render | cat -v)"
echo "  output=[$OUT]  (期望:空)"

echo
echo "=== 6) 繁中寬度截斷:窄欄 COLUMNS=30 ==="
/tmp/clt-sign bundle -key "$WORK/keys/priv.key" -kid k1 -in "$WORK/bundle.json" -out "$WORK/cache.json"
for i in 1 2 3 4 5; do
  OUT="$(COLUMNS=30 /tmp/clt-render | cat -v)"
  printf '  [%d] %s\n' "$i" "$OUT"
done
echo "DONE"
