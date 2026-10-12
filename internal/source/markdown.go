package source

import (
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// NewMarkdown indexes a directory of Markdown notes, e.g. an Obsidian vault.
func NewMarkdown(name, root string, exclude []string) DocumentSource {
	return &fileSource{name: name, root: root, exclude: exclude, accept: isMarkdown, load: loadMarkdown}
}

func isMarkdown(rel string) bool {
	ext := strings.ToLower(path.Ext(rel))
	return ext == ".md" || ext == ".markdown"
}

func loadMarkdown(rel string, data []byte) Document {
	content := string(data)
	meta := frontmatter(content)
	meta["language"] = "markdown"
	title := meta["title"]
	if title == "" {
		title = strings.TrimSuffix(path.Base(rel), path.Ext(rel))
	}
	delete(meta, "title")
	return Document{Title: title, Content: content, Metadata: meta}
}

// frontmatter extracts scalar and list fields from a leading YAML block. The block stays in the
// content so line numbers remain accurate and tags stay searchable.
func frontmatter(content string) map[string]string {
	meta := map[string]string{}
	if !strings.HasPrefix(content, "---\n") {
		return meta
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return meta
	}
	var raw map[string]any
	if yaml.Unmarshal([]byte(content[4:4+end]), &raw) != nil {
		return meta
	}
	for k, v := range raw {
		switch v := v.(type) {
		case []any:
			parts := make([]string, len(v))
			for i, p := range v {
				parts[i] = fmt.Sprint(p)
			}
			meta[k] = strings.Join(parts, ",")
		case map[string]any, nil:
		default:
			meta[k] = fmt.Sprint(v)
		}
	}
	return meta
}
