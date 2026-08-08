# Cli Times — 驗證結果與 launch-gating 待辦(2026-08-08)

## A. 已自動化驗證(通過)

| 項目 | 方法 | 結果 |
|---|---|---|
| 消毒器拒絕所有攻擊 payload | `go test ./internal/sanitize` 單元 | ✓ ESC/OSC/CSI/DCS、換行、C1、Unicode Tag、bidi、zero-width、無效 UTF-8、隱形格式/結合字元、未白名單 emoji 全部整條拒收 |
| 消毒器接受 EN + 繁中 正常內容 | 同上 | ✓ 含 NFKC 組合(e+́→é)、全形折疊(ＡＢＣ→ABC) |
| C1 不誤殺中文(對抗審查抓的 bug) | `TestCJKContinuationBytesNotMistakenForC1` | ✓ 一二三 通過(C1 在 codepoint 層檢查,非 raw byte) |
| 隱形字元走私(對抗審查抓的兩個 HIGH) | `TestRejectsInvisibleFormatAndCombining` | ✓ 軟連字號 U+00AD、結合符號、word-joiner、Mongolian FVS 全拒(按 Unicode 類別 Cf/Mn/Mc/Me 拒絕) |
| 消毒器三不變量(輸出⊆白名單 × 寬度≤上限 × 單行 × 合法 UTF-8) | `FuzzClean`,**230 萬次執行** | ✓ 零違反 |
| 寬度截斷不切斷全形字 / 不切斷色碼重置 | 單元 + renderer 測試 | ✓ 內容先截斷再包 SGR,重置碼永不被切 |
| Ed25519 驗章:竄改/錯金鑰/未知 kid/過期/rollback/超大 | `go test ./internal/feed` | ✓ 全部正確拒絕;**先驗章、後解析**(防 parser bomb) |
| renderer 輪播 / 跳過惡意行 / Ad: 前綴由 renderer 強制 / fail-closed | `go test ./cmd/renderer` + e2e-demo.sh | ✓ 惡意行被跳過、feed escape 零洩漏、贊助行強制 Ad:、竄改/過期/缺 cache 一律空白 |
| 惡意 COLUMNS(空/0/負/非數字/超大) | `TestColumnsHostileInput` | ✓ 一律落到安全預設並 clamp |
| renderer 不上網、不寫檔、不讀 stdin | 程式碼 + vet | ✓ 僅讀單一 cache 檔 |

執行:`export PATH=/opt/homebrew/bin:$PATH; cd ~/cli-times; go test ./...`
Fuzz:`go test ./internal/sanitize -run=xxx -fuzz=FuzzClean -fuzztime=30s`
端到端:`bash harness/e2e-demo.sh`

## B. Launch-gating 待辦 —— 需互動 session(無法在無頭環境完成)

這三項必須在真實互動的 Claude Code + 各終端機肉眼觀察,**上線前完成**。
已自動嘗試但確認無法自動化的原因記錄如下。

### B1. statusLine 內容是否落地到 transcript / telemetry(§1 誠實性關鍵)
- **自動化嘗試結果(2026-08-08)**:隔離 `CLAUDE_CONFIG_DIR` + canary statusLine +
  `claude -p` 印出模式 → canary **未**出現在 transcript;`--debug` 也**無** statusLine 執行痕跡。
  **結論:statusLine 在 `-p` 非互動模式不執行**,故此測試無法自動化。
- **待辦(人工)**:互動開 `claude`,statusLine 設為輸出唯一 canary,對話幾輪後
  `grep -r "$CANARY"` 掃 config dir 的 `*.jsonl`、`debug/`、OpenTelemetry;
  另存 binary strings 已知有 `tengu_status_line_result`(帶 char_length/visual_width)—
  確認是否含**文字內容本身**。命中 → 下修 ARCHITECTURE §1 的宣稱並在文案揭露。
- 腳本骨架:`harness/context-leak-test.sh`(需改為互動流程)。

### B2. Ansi parser 對非 SGR escape 是過濾/中和/穿透
- 消毒器**無論結果如何**都按「會穿透」設計(已完成),此測試僅為理解平台 + 日後回歸。
- **待辦(人工)**:`harness/statusline-probe.sh` 設為 statusLine,依 `case` 檔逐類輸出
  (sgr/osc8/osc52/title/cursor/dcs/tags/bidi/…),在 iTerm2 / Terminal.app / kitty /
  WezTerm / Ghostty / Windows Terminal / tmux 各觀察並記錄到本檔 §C 表格。
  osc52 可半自動:輸出前設剪貼簿已知值,觀察後 `pbpaste` 比對是否被覆寫。

### B3. COLUMNS 在 statusLine 子行程是否可靠存在
- renderer 已對缺失/惡意值 fail-safe 到預設 80(已測)。
- **待辦(人工)**:各終端機確認 `COLUMNS` 是否真被 Claude Code 設進 statusLine 子行程;
  若否,評估是否改用其他寬度來源(仍不讀 stdin 敏感欄位)。

## C. 終端機相容矩陣(B2 觀察結果,待填)

| case \ 終端機 | iTerm2 | Terminal.app | kitty | WezTerm | Ghostty | Win Terminal | tmux |
|---|---|---|---|---|---|---|---|
| sgr(基準) | | | | | | | |
| osc8 | | | | | | | |
| osc52(剪貼簿) | | | | | | | |
| title | | | | | | | |
| cursor/clear | | | | | | | |
| tags/bidi/zwsp | | | | | | | |

填法:`通過`(平台過濾掉)/ `穿透`(到達終端機造成效果)/ `n/a`。
無論結果,消毒器都已擋在前面 —— 此表是平台行為的存證與回歸基準。
