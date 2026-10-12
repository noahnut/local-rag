package source

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const runbookStorage = `<h1>Deploy</h1><p>Run the <strong>Jenkins</strong> job.</p>` +
	`<ac:structured-macro ac:name="code"><ac:parameter ac:name="language">bash</ac:parameter>` +
	`<ac:plain-text-body><![CDATA[ansible-playbook site.yml --limit "pop<1>"]]></ac:plain-text-body></ac:structured-macro>` +
	`<ac:structured-macro ac:name="info"><ac:parameter ac:name="title">Heads up</ac:parameter>` +
	`<ac:rich-text-body><p>China needs DISTRIBUTE_TO_CHINA_DP.</p></ac:rich-text-body></ac:structured-macro>` +
	`<table><tbody><tr><th>Env</th><th>Owner</th></tr><tr><td>Fed</td><td>Santosh</td></tr></tbody></table>`

// fakeConfluence serves a space "OBS" with two pages split across two list pages.
type fakeConfluence struct {
	mu        sync.Mutex
	bodyCalls map[string]int
	version   int
}

func (f *fakeConfluence) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/wiki/api/v2/spaces", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me@example.com" || p != "tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("keys") != "OBS" {
			fmt.Fprint(w, `{"results":[]}`)
			return
		}
		fmt.Fprint(w, `{"results":[{"id":"42","key":"OBS"}]}`)
	})
	mux.HandleFunc("/wiki/api/v2/spaces/42/pages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		v := f.version
		f.mu.Unlock()
		if r.URL.Query().Get("cursor") == "" {
			fmt.Fprintf(w, `{"results":[{"id":"1","title":"Runbook","version":{"number":%d,"createdAt":"2025-01-02T03:04:05Z"}}],
				"_links":{"next":"/wiki/api/v2/spaces/42/pages?cursor=abc"}}`, v)
			return
		}
		fmt.Fprint(w, `{"results":[{"id":"2","title":"Empty","version":{"number":1,"createdAt":"2025-01-01T00:00:00Z"}}],"_links":{}}`)
	})
	mux.HandleFunc("/wiki/api/v2/pages/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/wiki/api/v2/pages/")
		if r.URL.Query().Get("body-format") != "storage" {
			t.Errorf("body-format = %q", r.URL.Query().Get("body-format"))
		}
		f.mu.Lock()
		f.bodyCalls[id]++
		v := f.version
		f.mu.Unlock()
		body := ""
		if id == "1" {
			body = runbookStorage
		}
		fmt.Fprintf(w, `{"id":%q,"title":"Page %s","version":{"number":%d,"createdAt":"2025-01-02T03:04:05Z"},
			"body":{"storage":{"value":%q}},"_links":{"webui":"/spaces/OBS/pages/%s"}}`, id, id, v, body, id)
	})
	return mux
}

func TestConfluenceFetchAndIncremental(t *testing.T) {
	ctx := context.Background()
	fake := &fakeConfluence{bodyCalls: map[string]int{}, version: 3}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()
	c := NewConfluence("wiki", srv.URL+"/", []string{"OBS"}, "me@example.com", "tok")

	docs, present, err := c.FetchChanged(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || len(present) != 2 {
		t.Fatalf("want 2 docs across both list pages, got %d docs, %d present", len(docs), len(present))
	}
	d := docs[0]
	if d.ID != "wiki:1" || d.Title != "Page 1" || d.URL != srv.URL+"/wiki/spaces/OBS/pages/1" {
		t.Errorf("doc fields: %+v", d)
	}
	if d.Metadata["space"] != "OBS" || d.Metadata["version"] != "3" || d.Metadata["updated_at"] != "2025-01-02T03:04:05Z" {
		t.Errorf("metadata: %v", d.Metadata)
	}
	for _, want := range []string{
		"# Deploy",
		"**Jenkins**",
		"```bash\nansible-playbook site.yml --limit \"pop<1>\"\n```",
		"China needs DISTRIBUTE_TO_CHINA_DP.",
		"| Fed | Santosh |",
	} {
		if !strings.Contains(d.Content, want) {
			t.Errorf("markdown missing %q:\n%s", want, d.Content)
		}
	}
	if strings.Contains(d.Content, "Heads up") || strings.Contains(d.Content, "CDATA") {
		t.Errorf("macro parameters or CDATA leaked:\n%s", d.Content)
	}

	// Same versions: nothing is downloaded again.
	docs, _, _ = c.FetchChanged(ctx, present)
	if len(docs) != 0 || fake.bodyCalls["1"] != 1 {
		t.Errorf("unchanged pages re-fetched: %d docs, %d body calls", len(docs), fake.bodyCalls["1"])
	}

	// A new version of page 1 only.
	fake.mu.Lock()
	fake.version = 4
	fake.mu.Unlock()
	docs, _, _ = c.FetchChanged(ctx, present)
	if len(docs) != 1 || docs[0].ID != "wiki:1" || fake.bodyCalls["2"] != 1 {
		t.Errorf("want only page 1 re-fetched, got %d docs, page 2 body calls %d", len(docs), fake.bodyCalls["2"])
	}
}

func TestConfluenceErrors(t *testing.T) {
	srv := httptest.NewServer((&fakeConfluence{bodyCalls: map[string]int{}}).handler(t))
	defer srv.Close()

	_, err := NewConfluence("wiki", srv.URL, []string{"OBS"}, "me@example.com", "wrong").FetchDocuments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("bad token: %v", err)
	}
	_, err = NewConfluence("wiki", srv.URL, []string{"NOPE"}, "me@example.com", "tok").FetchDocuments(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown space: %v", err)
	}
}
