package source

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	ignore "github.com/sabhiram/go-gitignore"
)

type Document struct {
	ID       string            // e.g. "myrepo:pkg/auth.go"
	Title    string            // title or file name
	Content  string            // cleaned Markdown or source code
	URL      string            // original link or absolute file path
	Source   string            // configured source name, e.g. "codebase", "notes"
	Metadata map[string]string // language, tags, author, updated_at ...
}

type DocumentSource interface {
	Name() string
	FetchDocuments(ctx context.Context) ([]Document, error)
}

type FileStamp struct {
	ModTime int64 // unix nanoseconds
	Size    int64
}

// Incremental is implemented by file-backed sources so unchanged files are skipped without being
// read. known and present are keyed by document ID. A changed file that can no longer be indexed
// (e.g. it became binary) is returned with empty Content so its old chunks get removed.
type Incremental interface {
	FetchChanged(ctx context.Context, known map[string]FileStamp) (changed []Document, present map[string]FileStamp, err error)
}

type fileSource struct {
	name    string
	root    string
	exclude []string
	accept  func(rel string) bool
	load    func(rel string, data []byte) Document // fills Title, Content, Metadata
}

func (f *fileSource) Name() string { return f.name }

func (f *fileSource) FetchDocuments(ctx context.Context) ([]Document, error) {
	changed, _, err := f.FetchChanged(ctx, nil)
	if err != nil {
		return nil, err
	}
	var docs []Document
	for _, d := range changed {
		if d.Content != "" {
			docs = append(docs, d)
		}
	}
	return docs, nil
}

func (f *fileSource) FetchChanged(ctx context.Context, known map[string]FileStamp) ([]Document, map[string]FileStamp, error) {
	present := map[string]FileStamp{}
	var changed []Document
	err := walkFiles(ctx, f.root, f.exclude, f.accept, func(rel, abs string, info fs.FileInfo) error {
		id := f.name + ":" + rel
		stamp := FileStamp{ModTime: info.ModTime().UnixNano(), Size: info.Size()}
		present[id] = stamp
		if old, ok := known[id]; ok && old == stamp {
			return nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		doc := f.load(rel, data)
		doc.ID, doc.URL, doc.Source = id, abs, f.name
		changed = append(changed, doc)
		return nil
	})
	return changed, present, err
}

const maxFileSize = 512 << 10

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "__pycache__": true,
}

// walkFiles visits regular files under root that are not hidden, gitignored (root .gitignore only),
// excluded, or larger than maxFileSize, and for which accept(rel) is true.
func walkFiles(ctx context.Context, root string, exclude []string, accept func(rel string) bool, fn func(rel, abs string, info fs.FileInfo) error) error {
	ign := loadIgnore(root, exclude)
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || ign.MatchesPath(rel+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || ign.MatchesPath(rel) || !accept(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}
		return fn(rel, p, info)
	})
}

func loadIgnore(root string, exclude []string) *ignore.GitIgnore {
	var lines []string
	if data, err := os.ReadFile(filepath.Join(root, ".gitignore")); err == nil {
		lines = strings.Split(string(data), "\n")
	}
	return ignore.CompileIgnoreLines(append(lines, exclude...)...)
}
