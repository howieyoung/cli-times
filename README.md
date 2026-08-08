# Cli Times

CLI 等待區的 AI 日報 —— 在 AI coding CLI(先 Claude Code 的 statusLine)等待回應時,
輪播一行由人工策展的 AI 新聞 / tips。免費使用;付費可追蹤特定議題;贊助管道備而不用。

**實驗性專案**,與 Protico 完全切開。對外上線動作(marketplace 上架、宣傳)在 2026-08-15 之後。

## 設計原則:拒絕 kickbacks 拿走的每一項能力

內容是**純顯示資訊**,不可能變成指令。安全保證分三層:
- **由建構保證**:renderer 無網路 / 無 shell / 無寫檔;色碼只出自常數表,feed 位元組
  永不被拼進 escape 序列;永不輸出 OSC 8 / OSC 52 / 標題序列。
- **靠測試擔保**:拒絕式消毒器(先 UTF-8 解碼 → codepoint 層擋控制字元/C1 →
  按 Unicode 類別擋格式/結合字元 → 正面白名單 → NFKC → 寬度截斷),管線端與
  renderer 端各實作一次,property-based fuzz 驗四不變量。
- **最小化 + 可驗證**:程式碼永不自我更新;絕不寫 `~/.claude/settings.json`;
  Ed25519 簽章先驗後解析、fail-closed;零客戶端遙測。

## 佈局

```
cmd/renderer     statusLine 腳本:讀 cache → 驗章 → 選行 → 消毒 → 印一行(Go 靜態二進位)
cmd/updater      唯一觸網元件:每 ~6h 抓簽章 feed、先驗後原子寫入、fail-closed
cmd/sign         離線簽章工具:keygen / bundle(私鑰永不上線)
cmd/curate       半自動內容管線:crawl → 起草 → 風險標記 → 產草稿
internal/sanitize 安全核心:拒絕式消毒器 + property fuzz
internal/feed    簽章 bundle 格式 + Ed25519 驗證(先驗後解析、大小上限、防重放)
packaging/       Homebrew formula + launchd/systemd 定時器範本
harness/         escape-sequence 探針、端到端 demo、context-leak 測試、RESULTS.md
feed-bundles/    範例 / 產出的內容 bundle
```

## 開發

```bash
export PATH=/opt/homebrew/bin:$PATH
go test ./...                                              # 全測試
go test ./internal/sanitize -run=xxx -fuzz=FuzzClean -fuzztime=30s   # 消毒器 fuzz
bash harness/e2e-demo.sh                                   # 端到端 + fail-closed 驗證
```

## 狀態(2026-08-08)

- ✅ 安全核心(消毒器 / 驗章 / renderer)建置並通過測試 + 230 萬次 fuzz;經兩輪對抗性審查
  (架構層 16 項、程式碼層 5 項)全數修訂。
- ⏳ 三項 launch-gating 實測需互動 session 完成(見 `harness/RESULTS.md §B`)。
- ⏳ 內容管線(爬蟲 → 去重 → AI 起草 → 人工審 → 簽章)、updater、安裝流程。

完整技術架構、內容守則與研究基礎為內部文件,不隨公開源碼發佈。
