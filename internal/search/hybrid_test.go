package search

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/store"
)

func TestRRF(t *testing.T) {
	got := rrf(ranked{[]string{"a", "b", "c"}, 1}, ranked{[]string{"c", "a"}, 1})
	if got[0].id != "a" || got[1].id != "c" || got[2].id != "b" {
		t.Errorf("order = %v", got)
	}
	// Unweighted these tie and "b" wins on id order; the keyword weight must flip it.
	got = rrf(ranked{[]string{"z"}, 2}, ranked{[]string{"b"}, 1})
	if got[0].id != "z" {
		t.Errorf("weighted keyword hit should win, order = %v", got)
	}
}

func TestLooksLikeIdentifier(t *testing.T) {
	for q, want := range map[string]bool{
		"parseConfig": true, "MAX_SIZE": true, "pkg.Func": true,
		"config": false, "how does it work": false, "使用者": false,
	} {
		if got := looksLikeIdentifier(q); got != want {
			t.Errorf("looksLikeIdentifier(%q) = %v, want %v", q, got, want)
		}
	}
}

// keyEmbedder maps texts containing a keyword to a fixed direction.
type keyEmbedder struct{ fail bool }

func (k keyEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if k.fail {
		return nil, errors.New("ollama down")
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case strings.Contains(t, "login") || strings.Contains(t, "authenticate"):
			out[i] = []float32{1, 0}
		default:
			out[i] = []float32{0, 1}
		}
	}
	return out, nil
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mk := func(id, content string, vec ...float32) store.Record {
		return store.Record{Chunk: chunk.Chunk{ID: id, DocID: id, Source: "repo", Title: id, URL: id, Content: content, StartLine: 1, EndLine: 1}, Hash: id, Embedding: vec}
	}
	err = st.Upsert(context.Background(), []store.Record{
		mk("auth", "func authenticate(user, password string) bool", 1, 0),
		mk("server", "func startServer(port int)", 0, 1),
		mk("big", strings.Repeat("padding words ", 400)+"startServer", 0.1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSearchSemanticAndKeyword(t *testing.T) {
	st := newStore(t)
	s := New(st, keyEmbedder{}, "")
	ctx := context.Background()

	res, err := s.Search(ctx, "how does login work", Options{TopK: 1, MaxTokens: 1000})
	if err != nil || len(res) != 1 || res[0].Chunk.ID != "auth" {
		t.Errorf("semantic query: %+v %v", res, err)
	}
	res, _ = s.Search(ctx, "startServer", Options{TopK: 1, MaxTokens: 1000})
	if len(res) != 1 || res[0].Chunk.ID != "server" {
		t.Errorf("identifier query: %+v", res)
	}
}

func TestSearchTokenBudget(t *testing.T) {
	s := New(newStore(t), keyEmbedder{}, "")
	res, err := s.Search(context.Background(), "startServer", Options{TopK: 5, MaxTokens: 300})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, r := range res {
		if r.Chunk.ID == "big" {
			t.Error("chunk exceeding the budget should be skipped")
		}
		total += chunk.EstimateTokens(r.Chunk.Content)
	}
	if total > 300 {
		t.Errorf("total tokens %d > budget", total)
	}
}

func TestSearchFallsBackToKeyword(t *testing.T) {
	s := New(newStore(t), keyEmbedder{fail: true}, "")
	res, err := s.Search(context.Background(), "authenticate", Options{TopK: 3, MaxTokens: 1000})
	if err != nil || len(res) == 0 || res[0].Chunk.ID != "auth" {
		t.Errorf("fallback: %+v %v", res, err)
	}
}

func TestVectorCacheReloadsOnVersionChange(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	s := New(st, keyEmbedder{}, "")
	if _, err := s.Search(ctx, "login", Options{TopK: 1, MaxTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	st.Upsert(ctx, []store.Record{{Chunk: chunk.Chunk{ID: "new", DocID: "new", Source: "repo", Content: "x"}, Hash: "n", Embedding: []float32{1, 0}}})
	st.SetMeta(ctx, "version", "2")
	vecs, _ := s.loadVectors(ctx)
	if len(vecs) != 4 {
		t.Errorf("cache not reloaded: %d vectors", len(vecs))
	}
}
