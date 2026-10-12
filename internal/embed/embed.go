package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

func New(provider, model, url string) (Embedder, error) {
	switch provider {
	case "", "ollama":
		if url == "" {
			url = "http://localhost:11434"
		}
		return &Ollama{URL: url, Model: model}, nil
	case "openai":
		if url == "" {
			url = "https://api.openai.com/v1"
		}
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("OPENAI_API_KEY is not set")
		}
		return &OpenAI{URL: url, Model: model, APIKey: key}, nil
	}
	return nil, fmt.Errorf("unknown embedder provider %q", provider)
}

type Ollama struct {
	URL   string
	Model string
}

func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	req := map[string]any{"model": o.Model, "input": texts}
	if err := postJSON(ctx, o.URL+"/api/embed", "", req, &out); err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: got %d embeddings for %d inputs", len(out.Embeddings), len(texts))
	}
	return out.Embeddings, nil
}

type OpenAI struct {
	URL    string
	Model  string
	APIKey string
}

func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	req := map[string]any{"model": o.Model, "input": texts}
	if err := postJSON(ctx, o.URL+"/embeddings", o.APIKey, req, &out); err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("openai embed: got %d embeddings for %d inputs", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(vecs) {
			return nil, fmt.Errorf("openai embed: index %d out of range", d.Index)
		}
		vecs[d.Index] = d.Embedding
	}
	return vecs, nil
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

func postJSON(ctx context.Context, url, bearer string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, bytes.TrimSpace(msg))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
