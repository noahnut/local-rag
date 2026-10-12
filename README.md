# local-rag

A local hybrid-search (vector + BM25) MCP server, built as a single Go binary with no CGO. It lets Claude Code fetch only the relevant top-K chunks instead of reading whole files or directories into its context window.

- Storage: SQLite (`modernc.org/sqlite`) + FTS5; vectors are stored as BLOBs and scored with cosine similarity in memory.
- Embeddings: Ollama (default `nomic-embed-text`) or OpenAI `text-embedding-3-small`.
- Fusion: Reciprocal Rank Fusion. When the query is a single identifier (`parseConfig`, `MAX_SIZE`), exact whole-token keyword hits are preferred.
- CJK: characters are indexed one by one, and queries match them as two-character phrases.

## Install

```sh
CGO_ENABLED=0 go build -o ~/bin/local-rag ./cmd/local-rag
ollama pull nomic-embed-text
mkdir -p ~/.config/local-rag && cp config.example.yaml ~/.config/local-rag/config.yaml   # then edit sources
```

## Usage

```sh
local-rag index                 # incremental: re-embeds only changed chunks and removes deleted ones
local-rag index --rebuild       # rebuild from scratch (also happens automatically when the embedding model changes)
local-rag search -k 5 "how is auth handled"     # for debugging in the terminal
claude mcp add local-rag -- ~/bin/local-rag serve
```

## Automatic updates

Before each `search`, `serve` checks for changes (at most once every 30 seconds per server), so you rarely need to run `index` by hand:

- Files whose modification time and size are unchanged are only stat'ed, not read.
- Up to 200 changed chunks: the index is updated inline before the query runs.
- More than 200 (e.g. after switching branches): the update runs in the background, the query is answered from the existing index, and the result starts with a note saying so.
- A large source that has never been indexed is not built from inside the server; you are told to run `local-rag index` instead.
- When several Claude Code sessions share one database, a lock in SQLite keeps them from embedding the same changes twice.

The first index build and `--rebuild` still go through the CLI.

## MCP tools

| tool | parameters | description |
|---|---|---|
| `search` | `query`, `top_k`=5, `source`, `max_tokens`=1200 | Returns `path:start-end \| id=...` plus chunk content, capped by the token budget |
| `get_chunk` | `id` | Fetches a single chunk |
| `list_sources` | — | Chunk count per source |

If the embedding service is unreachable, `search` falls back to BM25-only results.

## Sources

| type | content | change detection |
|---|---|---|
| `codebase` | Source code directory; respects the root `.gitignore` | File modification time and size |
| `local` | Markdown / Obsidian vault; frontmatter becomes metadata | File modification time and size |
| `confluence` | Confluence Cloud spaces (REST API v2), pages converted to Markdown | Page version |

Confluence credentials are read only from environment variables: `CONFLUENCE_EMAIL` + `CONFLUENCE_API_TOKEN` (Cloud basic auth), or `CONFLUENCE_API_TOKEN` alone to send it as a Bearer personal access token. By default `serve` re-checks Confluence at most once an hour (`refresh_interval`); if it can't connect, it logs the error and the other sources are unaffected. Set the variables in the shell that launches Claude Code so the MCP server inherits them. Passing them with `claude mcp add -e` also works, but stores the token in plain text in `~/.claude.json`.

To add another source (e.g. Notion), implement `internal/source.DocumentSource` (`Name()`, `FetchDocuments()`) and register it in `Source.Build` in `internal/config`. If unchanged documents can be detected cheaply, also implement `source.Incremental`.
