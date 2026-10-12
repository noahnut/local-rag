# local-rag

本機混合檢索（向量 + BM25）的 MCP Server，單一 Go 二進位檔、無 CGO。讓 Claude Code 只取回相關的 Top-K chunks，而不是把整個檔案或目錄讀進 context。

- 儲存：SQLite（`modernc.org/sqlite`）+ FTS5；向量以 BLOB 存放，查詢時在記憶體做 cosine。
- Embedding：Ollama（預設 `nomic-embed-text`）或 OpenAI `text-embedding-3-small`。
- 融合：Reciprocal Rank Fusion；查詢若是單一識別符（`parseConfig`、`MAX_SIZE`），會優先採用完整 token 的關鍵字命中。
- 中文：CJK 字元逐字索引，查詢以雙字詞組比對。

## 安裝

```sh
CGO_ENABLED=0 go build -o ~/bin/local-rag ./cmd/local-rag
ollama pull nomic-embed-text
mkdir -p ~/.config/local-rag && cp config.example.yaml ~/.config/local-rag/config.yaml   # 編輯 sources
```

## 使用

```sh
local-rag index                 # 增量索引：只重新 embed 有變動的 chunk，並刪除已消失的
local-rag index --rebuild       # 換 embedding 模型後重建（模型改變時也會自動清空）
local-rag search -k 5 "how is auth handled"     # 終端機除錯用
claude mcp add local-rag -- ~/bin/local-rag serve
```

## 自動更新

`serve` 在每次 `search` 前會檢查變動（同一個 server 30 秒內最多檢查一次），平常不用手動重跑 `index`：

- 修改時間和大小都沒變的檔案只做 stat，不讀內容。
- 變動在 200 個 chunk 以內：當場更新後再查詢。
- 超過 200 個（例如切換 branch）：在背景更新，這次先用舊索引回答，結果開頭會附上提示。
- 從未索引過的大型來源不會在 server 裡自動建立，會提示改用 `local-rag index`。
- 多個 Claude Code 會話共用同一個資料庫時，以 SQLite 中的鎖避免重複 embed。

第一次建立索引和 `--rebuild` 仍然用 CLI。

## MCP tools

| tool | 參數 | 說明 |
|---|---|---|
| `search` | `query`, `top_k`=5, `source`, `max_tokens`=1200 | 回傳 `path:start-end \| id=...` 加上 chunk 內容，總量受 token 預算限制 |
| `get_chunk` | `id` | 取回單一 chunk |
| `list_sources` | — | 各來源的 chunk 數 |

Embedding 服務無法連線時，`search` 會退回只用 BM25 的結果。

## 新增資料來源

實作 `internal/source.DocumentSource`（`Name()`、`FetchDocuments()`），並在 `internal/config` 的 `Source.Build` 註冊即可（例如 Confluence、Notion）。
