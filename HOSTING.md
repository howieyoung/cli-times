# Cli Times — feed hosting (Cloudflare) 操作手冊

目標:把**已簽章**的 `dist/cache.json` 放到一個 CDN 網址,讓 updater 每 ~6h 抓。
整個內容的正確性靠 Ed25519 簽章,不靠主機 —— 所以主機被打也只會 fail-closed(空白),絕不會顯示壞內容。主機只要負責「可用 + 快」。

推薦 **Cloudflare Pages(直接上傳)** —— 免費、自帶 CDN、有 production 網址(`*.pages.dev`)、**沒有每 IP 速率限制**(GitHub raw 的 60 次/hr/IP 會讓 updater 間歇空白,所以不用它)。

---

## 一次性設定(約 10 分鐘,用你的 Cloudflare 帳號)

> 這些是**對外動作**,用你自己的帳號跑。在這個 session 裡,指令前面加 `!` 就能直接執行(例如 `! npx wrangler login`)。

### 1. 登入 Cloudflare(用 npx,不需安裝)
```bash
npx wrangler login             # 第一次會自動下載 wrangler,再開瀏覽器授權你的帳號
```
(以下所有 `wrangler ...` 指令都寫成 `npx wrangler ...`。若你偏好裸指令,可 `npm install -g wrangler` 一次,之後就能直接打 `wrangler`。)

### 2. 準備要發佈的資料夾
```bash
cd ~/cli-times
mkdir -p public
cp dist/cache.json public/feed.json      # feed 檔就叫 feed.json
# 設定快取:改動一天約兩次、updater 每 6h 抓,TTL 設短一點讓更新快點傳開
cat > public/_headers <<'EOF'
/feed.json
  Cache-Control: public, max-age=600
  Access-Control-Allow-Origin: *
EOF
```

### 3. 第一次部署(會建立專案)
```bash
npx wrangler pages deploy public --project-name cli-times-feed
```
完成後它會給你一個網址,例如:
```
https://cli-times-feed.pages.dev
```
你的 feed 就在 **`https://cli-times-feed.pages.dev/feed.json`** —— 這就是要 pin 進 updater 的網址。

### 4. 用這個網址重新編譯 binaries(把網址打進 updater)
```bash
CLI_TIMES_FEED_URL=https://cli-times-feed.pages.dev/feed.json bash build.sh feed-bundles/draft-<date>.json
```
`dist/` 會產出 renderer(`cli-times-*`)+ updater(`cli-times-update-*`),updater 已內建這個網址,不需環境變數。

### 5.(僅本機自己先測)裝 updater 的定時器
- macOS:把 `packaging/launchd/com.clitimes.updater.plist` 裡的路徑改成你的 `cli-times-update` 位置 → `cp` 到 `~/Library/LaunchAgents/` → `launchctl load ...`
- Linux:照 `packaging/systemd/` 裡的說明(`enable-linger` + `systemctl --user enable --now cli-times-update.timer`)

---

## 出刊(全自動,GitHub Actions —— 免筆電、免人工)

日常出刊由 `.github/workflows/publish.yml` 在 GitHub 伺服器上自動跑:每 12h 爬取 → OpenAI 起草 → 確定性風險標記 → **丟棄** flag 行 → Ed25519 簽章 → 部署到 Cloudflare Pages。無人在迴圈:被 flag 的行直接不收(永不顯示),survive 太少(<6)就拒發。你的筆電永遠不用開。

**一次性設定**(GitHub repo → Settings → Secrets and variables → Actions):

| Secret | 內容 |
|---|---|
| `OPENAI_API_KEY` | 你的 OpenAI key(去 OpenAI dashboard 設消費上限) |
| `CLI_TIMES_SIGNING_KEY` | `keys/priv.key` 的**內容**(單行 base64,`cat keys/priv.key` 整段貼進去);簽章金鑰在此變「熱」是刻意取捨——請對此 repo 開 2FA + 限縮 secret 存取 + environment protection |
| `CLOUDFLARE_API_TOKEN` | scope 到 Cloudflare Pages:Edit 的 token |
| `CLOUDFLARE_ACCOUNT_ID` | 你的 Cloudflare account id |

設好後到 **Actions → publish-feed → Run workflow**(workflow_dispatch)手動觸發第一次 → 當日簽章 feed 立刻上線(**連第一次都不用你在本機簽**)。之後 cron 每 12h 自己維持新鮮;用戶端 updater 下次輪詢(≤6h)自動抓到。

> **金鑰輪替**:換 key 需同步更新 binary / formula 內嵌的公鑰;目前沿用既有 k1。

### 手動出刊(備援,僅在 CI 掛掉時)
```bash
cd ~/cli-times && export OPENAI_API_KEY=sk-...
go run ./cmd/curate -n 18
go run ./cmd/sign bundle -key keys/priv.key -kid k1 \
    -in feed-bundles/draft-<date>.json -out public/feed.json \
    -strip-review -min-lines 6 -expires-hours 72
npx wrangler pages deploy public --project-name cli-times-feed
```

---

## 之後想升級(非必要)
- **自訂網域**(clitimes.dev):Cloudflare Pages → Custom domains 一鍵綁,updater 網址改成你的網域即可。
- **R2 取代 Pages**:內容量大時 R2 更適合(zero egress),但 `r2.dev` 是 dev-only,要綁自訂網域才適合 production。Pages 在試用期完全夠。

## 記得(資安 / 誠實)
- **落地頁文案要補一句**:renderer 零網路,但有獨立 updater 每 6h 只拉取 GET(CDN 看得到 IP,只收聚合數字、不送識別碼/內容)。
- feed 一定是**簽章後**的 `dist/cache.json`;絕不要把 `keys/priv.key` 或 `public/` 以外的東西上傳。
