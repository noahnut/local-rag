package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/source"
)

func TestSplitIdent(t *testing.T) {
	cases := map[string][]string{
		"parseHTTPConfig": {"parse", "HTTP", "Config"},
		"snake_case_name": {"snake", "case", "name"},
		"Base64Encode":    {"Base", "64", "Encode"},
		"simple":          {"simple"},
	}
	for in, want := range cases {
		if got := splitIdent(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitIdent(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMatchQuery(t *testing.T) {
	got := matchQuery(`how does parseConfig work? 使用者驗證`, true)
	want := `"how" OR "does" OR "parseconfig" OR "parse" OR "config" OR "work" OR "使 用" OR "用 者" OR "者 驗" OR "驗 證"`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := matchQuery(`parseConfig`, false); got != `"parseconfig"` {
		t.Errorf("exact: got %s", got)
	}
	if matchQuery(`"*()`, true) != "" {
		t.Error("punctuation-only query should produce no terms")
	}
}

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sub", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func rec(id, source, content string, vec ...float32) Record {
	return Record{
		Chunk: chunk.Chunk{ID: id, DocID: id, Source: source, Title: id, URL: "/x/" + id, Content: content,
			Metadata: map[string]string{"language": "go"}, StartLine: 1, EndLine: 3},
		Hash:      "h-" + content,
		Embedding: vec,
	}
}

func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	err := st.Upsert(ctx, []Record{
		rec("repo:a.go#0", "repo", "func parseConfig(path string) error", 1, 0),
		rec("repo:b.go#0", "repo", "func startServer() {}", 0, 1),
		rec("notes:n.md#0", "notes", "使用者驗證流程使用 JWT", 0.5, 0.5),
	})
	if err != nil {
		t.Fatal(err)
	}

	ids, err := st.Keyword(ctx, "parseConfig", "", 10, false)
	if err != nil || len(ids) == 0 || ids[0] != "repo:a.go#0" {
		t.Errorf("identifier search: %v %v", ids, err)
	}
	ids, _ = st.Keyword(ctx, "config", "", 10, false)
	if len(ids) != 1 || ids[0] != "repo:a.go#0" {
		t.Errorf("identifier sub-word search: %v", ids)
	}
	ids, _ = st.Keyword(ctx, "驗證", "", 10, false)
	if len(ids) != 1 || ids[0] != "notes:n.md#0" {
		t.Errorf("CJK search: %v", ids)
	}
	ids, _ = st.Keyword(ctx, "驗證", "repo", 10, false)
	if len(ids) != 0 {
		t.Errorf("source filter: %v", ids)
	}

	c, err := st.Get(ctx, "repo:a.go#0")
	if err != nil || c.Metadata["language"] != "go" || c.EndLine != 3 {
		t.Errorf("get: %+v %v", c, err)
	}
	vecs, _ := st.Vectors(ctx)
	if len(vecs) != 3 || len(vecs[0].Vec) != 2 {
		t.Errorf("vectors: %+v", vecs)
	}

	// Re-upserting the same id must replace, not duplicate, the FTS row.
	if err := st.Upsert(ctx, []Record{rec("repo:a.go#0", "repo", "func loadSettings()", 1, 0)}); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.Keyword(ctx, "parseConfig", "", 10, false); len(ids) != 0 {
		t.Errorf("stale FTS row after re-upsert: %v", ids)
	}

	if err := st.Delete(ctx, []string{"repo:b.go#0"}); err != nil {
		t.Fatal(err)
	}
	refs, _ := st.ChunkRefs(ctx, "repo")
	if len(refs) != 1 || refs["repo:a.go#0"] != (ChunkRef{DocID: "repo:a.go#0", Hash: "h-func loadSettings()"}) {
		t.Errorf("refs: %v", refs)
	}
	stats, _ := st.Stats(ctx)
	if len(stats) != 2 || stats[0].Source != "notes" || stats[1].Chunks != 1 {
		t.Errorf("stats: %+v", stats)
	}

	if err := st.SetFileStamps(ctx, "repo", map[string]source.FileStamp{"repo:a.go": {ModTime: 1, Size: 2}, "repo:b.go": {ModTime: 3, Size: 4}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFileStamps(ctx, "repo", map[string]source.FileStamp{"repo:a.go": {ModTime: 5, Size: 6}}, []string{"repo:b.go"}); err != nil {
		t.Fatal(err)
	}
	stamps, _ := st.FileStamps(ctx, "repo")
	if len(stamps) != 1 || stamps["repo:a.go"] != (source.FileStamp{ModTime: 5, Size: 6}) {
		t.Errorf("stamps: %v", stamps)
	}

	if err := st.Clear(ctx); err != nil {
		t.Fatal(err)
	}
	if ids, _ := st.Keyword(ctx, "JWT", "", 10, false); len(ids) != 0 {
		t.Errorf("clear left FTS rows: %v", ids)
	}
	if stamps, _ := st.FileStamps(ctx, "repo"); len(stamps) != 0 {
		t.Errorf("clear left file stamps: %v", stamps)
	}
}

func TestLease(t *testing.T) {
	ctx := context.Background()
	st := openTest(t)
	release, ok, err := st.TryLease(ctx, "l", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first lease: %v %v", ok, err)
	}
	if _, ok, _ := st.TryLease(ctx, "l", time.Minute); ok {
		t.Error("second lease must fail while held")
	}
	release()
	if _, ok, _ := st.TryLease(ctx, "l", time.Nanosecond); !ok {
		t.Error("lease must be available after release")
	}
	time.Sleep(time.Millisecond)
	if _, ok, _ := st.TryLease(ctx, "l", time.Minute); !ok {
		t.Error("expired lease must be re-acquirable")
	}
}
