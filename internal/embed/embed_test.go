package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "m" || len(req.Input) != 2 {
			t.Errorf("bad request %+v", req)
		}
		w.Write([]byte(`{"embeddings":[[1,2],[3,4]]}`))
	}))
	defer srv.Close()

	vecs, err := (&Ollama{URL: srv.URL, Model: "m"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[1][0] != 3 {
		t.Errorf("got %v", vecs)
	}
}

func TestOpenAIOrdersByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		w.Write([]byte(`{"data":[{"index":1,"embedding":[2]},{"index":0,"embedding":[1]}]}`))
	}))
	defer srv.Close()

	vecs, err := (&OpenAI{URL: srv.URL, Model: "m", APIKey: "k"}).Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if vecs[0][0] != 1 || vecs[1][0] != 2 {
		t.Errorf("got %v", vecs)
	}
}

func TestErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `model "x" not found`, http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := (&Ollama{URL: srv.URL, Model: "x"}).Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("want error")
	}
}
