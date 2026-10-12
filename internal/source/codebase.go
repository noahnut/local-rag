package source

import (
	"bytes"
	"path"
	"strings"
	"unicode/utf8"
)

var codeLanguages = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".jsx": "javascript", ".ts": "typescript", ".tsx": "typescript",
	".rs": "rust", ".java": "java", ".kt": "kotlin", ".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
	".cs": "csharp", ".rb": "ruby", ".php": "php", ".swift": "swift", ".scala": "scala", ".sh": "shell", ".sql": "sql",
	".proto": "protobuf", ".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".md": "markdown",
}

func NewCodebase(name, root string, exclude []string) DocumentSource {
	return &fileSource{name: name, root: root, exclude: exclude, accept: isCode, load: loadCode}
}

func isCode(rel string) bool {
	_, ok := codeLanguages[strings.ToLower(path.Ext(rel))]
	return ok
}

func loadCode(rel string, data []byte) Document {
	doc := Document{Title: rel}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return doc
	}
	doc.Content = string(data)
	doc.Metadata = map[string]string{"language": codeLanguages[strings.ToLower(path.Ext(rel))]}
	return doc
}
