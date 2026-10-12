package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noahfan/local-rag/internal/source"
	"github.com/noahfan/local-rag/internal/store"
)

type fakeSource struct{ docs []source.Document }

func (f *fakeSource) Name() string { return "fake" }
func (f *fakeSource) FetchDocuments(context.Context) ([]source.Document, error) {
	return f.docs, nil
}

type countingEmbedder struct{ calls, texts int }

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls++
	c.texts += len(texts)
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func newIndexer(t *testing.T) (*Indexer, *countingEmbedder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "i.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	emb := &countingEmbedder{}
	return &Indexer{Store: st, Embedder: emb, MaxTokens: 400, BatchSize: 2}, emb
}

func TestIncrementalIndex(t *testing.T) {
	ctx := context.Background()
	ix, emb := newIndexer(t)
	st := ix.Store
	src := &fakeSource{docs: []source.Document{
		{ID: "fake:a.go", Source: "fake", Title: "a.go", Content: "func a() {}"},
		{ID: "fake:b.go", Source: "fake", Title: "b.go", Content: "func b() {}"},
		{ID: "fake:c.go", Source: "fake", Title: "c.go", Content: "func c() {}"},
	}}

	stats, err := ix.Index(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embedded != 3 || emb.calls != 2 {
		t.Errorf("first run: %+v, embed calls %d (want 3 embedded in 2 batches)", stats, emb.calls)
	}
	v1, _ := st.Meta(ctx, "version")

	stats, _ = ix.Index(ctx, src)
	if stats.Embedded != 0 || stats.Deleted != 0 {
		t.Errorf("unchanged run should embed nothing: %+v", stats)
	}
	if v2, _ := st.Meta(ctx, "version"); v2 != v1 {
		t.Error("version must not change when nothing changed")
	}

	src.docs = []source.Document{
		{ID: "fake:a.go", Source: "fake", Title: "a.go", Content: "func a() { changed() }"},
		src.docs[1],
	}
	stats, _ = ix.Index(ctx, src)
	if stats.Embedded != 1 || stats.Deleted != 1 {
		t.Errorf("changed run: %+v (want 1 embedded, 1 deleted)", stats)
	}
	refs, _ := st.ChunkRefs(ctx, "fake")
	if len(refs) != 2 {
		t.Errorf("want 2 chunks left, got %v", refs)
	}
	if v3, _ := st.Meta(ctx, "version"); v3 == v1 {
		t.Error("version must change after updates")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileSourceSkipsUnchangedFiles(t *testing.T) {
	ctx := context.Background()
	ix, _ := newIndexer(t)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "func a() {}")
	writeFile(t, filepath.Join(root, "b.go"), "func b() {}")
	src := source.NewCodebase("repo", root, nil)

	stats, err := ix.Index(ctx, src)
	if err != nil || stats.Documents != 2 || stats.Embedded != 2 {
		t.Fatalf("first run: %+v %v", stats, err)
	}
	p, _ := ix.Plan(ctx, src)
	if p.Documents != 0 || p.Pending() != 0 || p.Fresh {
		t.Errorf("unchanged files must not be re-read: docs=%d pending=%d fresh=%v", p.Documents, p.Pending(), p.Fresh)
	}

	// Same content, new mtime: re-read but nothing to embed.
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(root, "a.go"), future, future)
	if stats, _ := ix.Index(ctx, src); stats.Documents != 1 || stats.Embedded != 0 {
		t.Errorf("touched file: %+v", stats)
	}

	writeFile(t, filepath.Join(root, "b.go"), "func b() { changed() }")
	os.Remove(filepath.Join(root, "a.go"))
	stats, _ = ix.Index(ctx, src)
	if stats.Documents != 1 || stats.Embedded != 1 || stats.Deleted != 1 {
		t.Errorf("edit + delete: %+v", stats)
	}
	stamps, _ := ix.Store.FileStamps(ctx, "repo")
	if _, ok := stamps["repo:a.go"]; ok || len(stamps) != 1 {
		t.Errorf("stamps after delete: %v", stamps)
	}
}

func newRefresher(t *testing.T, root string, syncLimit int) (*Refresher, *countingEmbedder) {
	t.Helper()
	ix, emb := newIndexer(t)
	ix.Store.SetMeta(context.Background(), "embed_model", "m")
	return &Refresher{
		Indexer:   ix,
		Sources:   []source.DocumentSource{source.NewCodebase("repo", root, nil)},
		Model:     "m",
		Interval:  time.Hour,
		SyncLimit: syncLimit,
		LeaseTTL:  time.Minute,
	}, emb
}

func waitIdle(r *Refresher) {
	r.mu.Lock()
	r.mu.Unlock()
}

func TestRefreshInlineAndThrottle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "func a() {}")
	r, emb := newRefresher(t, root, 10)

	if note := r.Refresh(ctx); note != "" {
		t.Errorf("small fresh source should index inline without a note, got %q", note)
	}
	if emb.texts != 1 {
		t.Errorf("embedded %d texts, want 1", emb.texts)
	}
	writeFile(t, filepath.Join(root, "b.go"), "func b() {}")
	r.Refresh(ctx)
	if emb.texts != 1 {
		t.Error("refresh within Interval must not re-check")
	}
	r.lastCheck = time.Time{}
	r.Refresh(ctx)
	if emb.texts != 2 {
		t.Errorf("after interval: embedded %d texts, want 2", emb.texts)
	}
}

func TestRefreshLargeChangeGoesToBackground(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "func a() {}")
	r, emb := newRefresher(t, root, 2)
	r.Refresh(ctx) // index a.go inline so the source is no longer fresh

	for _, n := range []string{"b", "c", "d"} {
		writeFile(t, filepath.Join(root, n+".go"), "func "+n+"() {}")
	}
	r.lastCheck = time.Time{}
	if note := r.Refresh(ctx); !strings.Contains(note, "background (3 chunks)") {
		t.Errorf("note = %q", note)
	}
	waitIdle(r)
	if emb.texts != 4 {
		t.Errorf("background apply embedded %d texts total, want 4", emb.texts)
	}
	if _, ok, _ := r.Indexer.Store.TryLease(ctx, leaseKey, time.Minute); !ok {
		t.Error("lease must be released after background apply")
	}
}

func TestRefreshSkipsLargeUnindexedSource(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		writeFile(t, filepath.Join(root, n+".go"), "func "+n+"() {}")
	}
	r, emb := newRefresher(t, root, 2)
	if note := r.Refresh(context.Background()); !strings.Contains(note, "not indexed yet") {
		t.Errorf("note = %q", note)
	}
	if emb.calls != 0 {
		t.Error("must not embed a large unindexed source from the server")
	}
}

func TestRefreshModelMismatch(t *testing.T) {
	r, emb := newRefresher(t, t.TempDir(), 10)
	r.Model = "other"
	if note := r.Refresh(context.Background()); !strings.Contains(note, "--rebuild") {
		t.Errorf("note = %q", note)
	}
	if emb.calls != 0 {
		t.Error("must not embed with a different model")
	}
}
