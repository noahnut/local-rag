package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/index"
	"github.com/noahfan/local-rag/internal/search"
	"github.com/noahfan/local-rag/internal/store"
)

func New(searcher *search.Searcher, refresher *index.Refresher, st *store.Store, version string) *server.MCPServer {
	s := server.NewMCPServer("local-rag", version, server.WithToolCapabilities(false))

	s.AddTool(mcp.NewTool("search",
		mcp.WithDescription("Hybrid semantic + keyword search over locally indexed codebases and notes. "+
			"Use this BEFORE reading whole files or grepping large directories: it returns only the most relevant "+
			"snippets with file paths and line ranges. Works for natural-language questions and exact identifiers."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Question, topic, or identifier to look up")),
		mcp.WithNumber("top_k", mcp.DefaultNumber(5), mcp.Description("Maximum number of chunks to return")),
		mcp.WithString("source", mcp.Description("Restrict to one indexed source name (see list_sources)")),
		mcp.WithNumber("max_tokens", mcp.DefaultNumber(1200), mcp.Description("Approximate token budget for all returned chunks")),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		note := refresher.Refresh(ctx)
		results, err := searcher.Search(ctx, query, search.Options{
			TopK:      max(1, req.GetInt("top_k", 5)),
			MaxTokens: max(100, req.GetInt("max_tokens", 1200)),
			Source:    req.GetString("source", ""),
		})
		if err != nil {
			return mcp.NewToolResultErrorFromErr("search failed", err), nil
		}
		var b strings.Builder
		if note != "" {
			b.WriteString("Note: " + note + "\n\n")
		}
		if len(results) == 0 {
			b.WriteString("No matching chunks.")
		}
		for i, r := range results {
			fmt.Fprintf(&b, "--- [%d] %s\n%s\n\n", i+1, header(r.Chunk), r.Chunk.Content)
		}
		return mcp.NewToolResultText(b.String()), nil
	})

	s.AddTool(mcp.NewTool("get_chunk",
		mcp.WithDescription("Fetch one indexed chunk by id (as shown in search results), e.g. to re-read it in full."),
		mcp.WithString("id", mcp.Required(), mcp.Description("Chunk id, e.g. myrepo:pkg/auth.go#2")),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		c, err := st.Get(ctx, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(header(c) + "\n" + c.Content), nil
	})

	s.AddTool(mcp.NewTool("list_sources",
		mcp.WithDescription("List the indexed sources and how many chunks each has."),
		mcp.WithReadOnlyHintAnnotation(true),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		stats, err := st.Stats(ctx)
		if err != nil {
			return mcp.NewToolResultErrorFromErr("list sources failed", err), nil
		}
		if len(stats) == 0 {
			return mcp.NewToolResultText("Index is empty. Run `local-rag index`."), nil
		}
		var b strings.Builder
		for _, s := range stats {
			fmt.Fprintf(&b, "%s: %d chunks\n", s.Source, s.Chunks)
		}
		return mcp.NewToolResultText(b.String()), nil
	})

	return s
}

func header(c chunk.Chunk) string {
	return fmt.Sprintf("%s:%d-%d | id=%s", c.URL, c.StartLine, c.EndLine, c.ID)
}
