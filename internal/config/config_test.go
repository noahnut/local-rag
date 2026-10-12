package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, yaml string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestLoadConfluence(t *testing.T) {
	cfg, err := load(t, `
sources:
  - {name: wiki, type: confluence, url: https://example.atlassian.net, spaces: [OBS, PLE]}
  - {name: fast, type: confluence, url: https://example.atlassian.net, spaces: [OBS], refresh_interval: 15m}
  - {type: local, path: notes}
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sources[0].RefreshInterval != time.Hour || cfg.Sources[1].RefreshInterval != 15*time.Minute {
		t.Errorf("intervals: %v, %v", cfg.Sources[0].RefreshInterval, cfg.Sources[1].RefreshInterval)
	}
	if cfg.Sources[2].Name != "local" || !filepath.IsAbs(cfg.Sources[2].Path) || cfg.Sources[2].RefreshInterval != 0 {
		t.Errorf("local source: %+v", cfg.Sources[2])
	}

	t.Setenv("CONFLUENCE_API_TOKEN", "")
	if _, err := cfg.Sources[0].Build(); err == nil || !strings.Contains(err.Error(), "CONFLUENCE_API_TOKEN") {
		t.Errorf("missing token: %v", err)
	}
	t.Setenv("CONFLUENCE_API_TOKEN", "tok")
	if src, err := cfg.Sources[0].Build(); err != nil || src.Name() != "wiki" {
		t.Errorf("build: %v %v", src, err)
	}
}

func TestLoadRejectsBadSources(t *testing.T) {
	for _, bad := range []string{
		"sources: [{type: confluence, spaces: [OBS]}]",
		"sources: [{type: confluence, url: https://x.atlassian.net}]",
		"sources: [{type: notion}]",
		"sources: [{type: local, path: a}, {type: local, path: b}]",
	} {
		if _, err := load(t, bad); err == nil {
			t.Errorf("want error for %s", bad)
		}
	}
}
