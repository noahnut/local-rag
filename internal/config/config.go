package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/noahfan/local-rag/internal/source"
)

type Config struct {
	DB             string   `yaml:"db"`
	ChunkMaxTokens int      `yaml:"chunk_max_tokens"`
	Embedder       Embedder `yaml:"embedder"`
	Sources        []Source `yaml:"sources"`
}

type Embedder struct {
	Provider       string `yaml:"provider"` // ollama | openai
	Model          string `yaml:"model"`
	URL            string `yaml:"url"`
	QueryPrefix    string `yaml:"query_prefix"`
	DocumentPrefix string `yaml:"document_prefix"`
}

type Source struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"` // codebase | local | confluence
	Path    string   `yaml:"path"`
	Exclude []string `yaml:"exclude"` // gitignore-style patterns

	URL    string   `yaml:"url"`    // confluence: site root, e.g. https://example.atlassian.net
	Spaces []string `yaml:"spaces"` // confluence: space keys

	// RefreshInterval is the minimum time between automatic re-checks by the server.
	// Remote sources default to 1h; local ones are checked on every throttled refresh.
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

func DefaultPath() string {
	return expandHome("~/.config/local-rag/config.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Config{
		DB:             "~/.local/share/local-rag/index.db",
		ChunkMaxTokens: 400,
		Embedder:       Embedder{Provider: "ollama", Model: "nomic-embed-text"},
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.DB = expandHome(cfg.DB)
	names := map[string]bool{}
	for i := range cfg.Sources {
		s := &cfg.Sources[i]
		if s.Name == "" {
			s.Name = s.Type
		}
		if names[s.Name] {
			return nil, fmt.Errorf("duplicate source name %q", s.Name)
		}
		names[s.Name] = true
		switch s.Type {
		case "codebase", "local":
			if s.Path, err = filepath.Abs(expandHome(s.Path)); err != nil {
				return nil, err
			}
		case "confluence":
			if s.URL == "" || len(s.Spaces) == 0 {
				return nil, fmt.Errorf("source %s: confluence needs url and spaces", s.Name)
			}
			if s.RefreshInterval == 0 {
				s.RefreshInterval = time.Hour
			}
		default:
			return nil, fmt.Errorf("source %d: type must be codebase, local or confluence, got %q", i, s.Type)
		}
	}
	return &cfg, nil
}

// Build creates the source. Confluence credentials come from CONFLUENCE_API_TOKEN and, for
// Confluence Cloud basic auth, CONFLUENCE_EMAIL; without an email the token is sent as a Bearer PAT.
func (s Source) Build() (source.DocumentSource, error) {
	switch s.Type {
	case "codebase":
		return source.NewCodebase(s.Name, s.Path, s.Exclude), nil
	case "confluence":
		token := os.Getenv("CONFLUENCE_API_TOKEN")
		if token == "" {
			return nil, fmt.Errorf("source %s: CONFLUENCE_API_TOKEN is not set", s.Name)
		}
		return source.NewConfluence(s.Name, s.URL, s.Spaces, os.Getenv("CONFLUENCE_EMAIL"), token), nil
	}
	return source.NewMarkdown(s.Name, s.Path, s.Exclude), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
