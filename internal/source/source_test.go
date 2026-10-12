package source

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodebaseFiltering(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		".gitignore":        "gen/\n*.pb.go\n",
		"main.go":           "package main",
		"pkg/auth.go":       "package pkg",
		"pkg/auth_test.go":  "package pkg",
		"pkg/api.pb.go":     "package pkg",
		"gen/out.go":        "package gen",
		"node_modules/x.js": "x",
		".hidden/secret.go": "package hidden",
		"image.png":         "\x89PNG",
		"bin.go":            "package\x00bin",
		"README.md":         "# readme",
	})
	docs, err := NewCodebase("repo", root, []string{"*_test.go"}).FetchDocuments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range docs {
		got = append(got, d.Title)
	}
	sort.Strings(got)
	want := []string{"README.md", "main.go", "pkg/auth.go"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	for _, d := range docs {
		if d.Title == "main.go" && (d.ID != "repo:main.go" || d.Metadata["language"] != "go" || d.URL != filepath.Join(root, "main.go")) {
			t.Errorf("unexpected doc %+v", d)
		}
	}
}

func TestMarkdownFrontmatter(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"notes/raft.md": "---\ntitle: Raft Notes\ntags: [consensus, distributed]\nauthor: me\n---\n# Raft\nleader election",
		"plain.md":      "# Plain\nbody",
		"skip.txt":      "not markdown",
	})
	docs, err := NewMarkdown("notes", root, nil).FetchDocuments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("want 2 docs, got %d", len(docs))
	}
	byID := map[string]Document{}
	for _, d := range docs {
		byID[d.ID] = d
	}
	raft := byID["notes:notes/raft.md"]
	if raft.Title != "Raft Notes" || raft.Metadata["tags"] != "consensus,distributed" || raft.Metadata["author"] != "me" {
		t.Errorf("frontmatter not parsed: %+v", raft)
	}
	if byID["notes:plain.md"].Title != "plain" {
		t.Errorf("title should default to file name, got %q", byID["notes:plain.md"].Title)
	}
}
