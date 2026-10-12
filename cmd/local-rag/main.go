package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/config"
	"github.com/noahfan/local-rag/internal/embed"
	"github.com/noahfan/local-rag/internal/index"
	"github.com/noahfan/local-rag/internal/mcpserver"
	"github.com/noahfan/local-rag/internal/search"
	"github.com/noahfan/local-rag/internal/source"
	"github.com/noahfan/local-rag/internal/store"
)

const version = "0.1.0"

const usage = `usage: local-rag <command> [flags]

commands:
  index   [--rebuild] [--source name]       index configured sources (incremental)
  serve                                     run the MCP server on stdio
  search  [-k 5] [--source name] [--max-tokens 1200] <query>

common flags:
  --config path   (default ~/.config/local-rag/config.yaml)
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "config file")
	rebuild := fs.Bool("rebuild", false, "drop the index and re-embed everything")
	only := fs.String("source", "", "only this source")
	topK := fs.Int("k", 5, "max results")
	maxTokens := fs.Int("max-tokens", 1200, "token budget for results")
	fs.Parse(os.Args[2:])

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	st, err := store.Open(cfg.DB)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()
	emb, err := embed.New(cfg.Embedder.Provider, cfg.Embedder.Model, cfg.Embedder.URL)
	if err != nil {
		log.Fatal(err)
	}

	switch cmd {
	case "index":
		err = runIndex(ctx, cfg, st, emb, *rebuild, *only)
	case "serve":
		searcher := search.New(st, emb, cfg.Embedder.QueryPrefix)
		err = server.ServeStdio(mcpserver.New(searcher, newRefresher(cfg, st, emb), st, version))
	case "search":
		err = runSearch(ctx, cfg, st, emb, strings.Join(fs.Args(), " "), search.Options{TopK: *topK, MaxTokens: *maxTokens, Source: *only})
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func modelKey(cfg *config.Config) string {
	return cfg.Embedder.Provider + "/" + cfg.Embedder.Model
}

func newIndexer(cfg *config.Config, st *store.Store, emb embed.Embedder) *index.Indexer {
	return &index.Indexer{Store: st, Embedder: emb, DocumentPrefix: cfg.Embedder.DocumentPrefix, MaxTokens: cfg.ChunkMaxTokens, BatchSize: 32}
}

func newRefresher(cfg *config.Config, st *store.Store, emb embed.Embedder) *index.Refresher {
	var sources []source.DocumentSource
	for _, sc := range cfg.Sources {
		sources = append(sources, sc.Build())
	}
	return &index.Refresher{
		Indexer:   newIndexer(cfg, st, emb),
		Sources:   sources,
		Model:     modelKey(cfg),
		Interval:  30 * time.Second,
		SyncLimit: 200,
		LeaseTTL:  15 * time.Minute,
	}
}

func runIndex(ctx context.Context, cfg *config.Config, st *store.Store, emb embed.Embedder, rebuild bool, only string) error {
	model := modelKey(cfg)
	prev, err := st.Meta(ctx, "embed_model")
	if err != nil {
		return err
	}
	if rebuild || (prev != "" && prev != model) {
		log.Printf("clearing index (embedding model %q -> %q)", prev, model)
		if err := st.Clear(ctx); err != nil {
			return err
		}
	}
	if err := st.SetMeta(ctx, "embed_model", model); err != nil {
		return err
	}

	ix := newIndexer(cfg, st, emb)
	for _, sc := range cfg.Sources {
		if only != "" && sc.Name != only {
			continue
		}
		stats, err := ix.Index(ctx, sc.Build())
		if err != nil {
			return fmt.Errorf("%s: %w", sc.Name, err)
		}
		log.Printf("%s: %d docs changed, %d chunks embedded, %d deleted", sc.Name, stats.Documents, stats.Embedded, stats.Deleted)
	}
	return nil
}

func runSearch(ctx context.Context, cfg *config.Config, st *store.Store, emb embed.Embedder, query string, o search.Options) error {
	if query == "" {
		return fmt.Errorf("search: query is required")
	}
	results, err := search.New(st, emb, cfg.Embedder.QueryPrefix).Search(ctx, query, o)
	if err != nil {
		return err
	}
	total := 0
	for i, r := range results {
		t := chunk.EstimateTokens(r.Chunk.Content)
		total += t
		fmt.Printf("--- [%d] %s:%d-%d | id=%s | score=%.4f | ~%d tokens\n%s\n\n", i+1, r.Chunk.URL, r.Chunk.StartLine, r.Chunk.EndLine, r.Chunk.ID, r.Score, t, r.Chunk.Content)
	}
	fmt.Printf("%d results, ~%d tokens\n", len(results), total)
	return nil
}
