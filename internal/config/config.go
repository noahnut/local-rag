package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Type    string   `yaml:"type"` // codebase | local
	Path    string   `yaml:"path"`
	Exclude []string `yaml:"exclude"` // gitignore-style patterns
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
		if s.Type != "codebase" && s.Type != "local" {
			return nil, fmt.Errorf("source %d: type must be codebase or local, got %q", i, s.Type)
		}
		if s.Name == "" {
			s.Name = s.Type
		}
		if names[s.Name] {
			return nil, fmt.Errorf("duplicate source name %q", s.Name)
		}
		names[s.Name] = true
		if s.Path, err = filepath.Abs(expandHome(s.Path)); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

func (s Source) Build() source.DocumentSource {
	if s.Type == "codebase" {
		return source.NewCodebase(s.Name, s.Path, s.Exclude)
	}
	return source.NewMarkdown(s.Name, s.Path, s.Exclude)
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}
