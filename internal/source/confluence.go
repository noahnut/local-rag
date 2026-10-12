package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
)

// Confluence indexes the current pages of Confluence Cloud spaces through the REST API v2.
type Confluence struct {
	name   string
	site   string // e.g. https://example.atlassian.net
	spaces []string
	email  string // empty: token is sent as a Bearer personal access token
	token  string
	client *http.Client
	toMD   *converter.Converter
}

func NewConfluence(name, site string, spaces []string, email, token string) *Confluence {
	return &Confluence{
		name:   name,
		site:   strings.TrimRight(site, "/"),
		spaces: spaces,
		email:  email,
		token:  token,
		client: &http.Client{Timeout: time.Minute},
		toMD: converter.NewConverter(
			converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin()),
			// The output is read, not rendered: escaping would put backslashes inside identifiers.
			converter.WithEscapeMode(converter.EscapeModeDisabled),
		),
	}
}

func (c *Confluence) Name() string { return c.name }

type cfPage struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Version struct {
		Number    int64     `json:"number"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"version"`
	Body struct {
		Storage struct {
			Value string `json:"value"`
		} `json:"storage"`
	} `json:"body"`
	Links struct {
		WebUI string `json:"webui"`
	} `json:"_links"`
}

func (c *Confluence) FetchDocuments(ctx context.Context) ([]Document, error) {
	changed, _, err := c.FetchChanged(ctx, nil)
	return changed, err
}

// FetchChanged lists pages with their version only, and downloads bodies just for pages whose
// version differs from known. The stamp holds the version time and number instead of file mtime/size.
func (c *Confluence) FetchChanged(ctx context.Context, known map[string]FileStamp) ([]Document, map[string]FileStamp, error) {
	present := map[string]FileStamp{}
	var changed []Document
	for _, key := range c.spaces {
		spaceID, err := c.spaceID(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		pages, err := c.listPages(ctx, spaceID)
		if err != nil {
			return nil, nil, fmt.Errorf("list pages in %s: %w", key, err)
		}
		for _, p := range pages {
			id := c.name + ":" + p.ID
			stamp := FileStamp{ModTime: p.Version.CreatedAt.UnixNano(), Size: p.Version.Number}
			present[id] = stamp
			if old, ok := known[id]; ok && old == stamp {
				continue
			}
			doc, err := c.fetchPage(ctx, key, p.ID)
			if err != nil {
				return nil, nil, err
			}
			doc.ID = id
			changed = append(changed, doc)
		}
	}
	return changed, present, nil
}

func (c *Confluence) spaceID(ctx context.Context, key string) (string, error) {
	var out struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/wiki/api/v2/spaces?keys="+url.QueryEscape(key), &out); err != nil {
		return "", fmt.Errorf("look up space %s: %w", key, err)
	}
	if len(out.Results) == 0 {
		return "", fmt.Errorf("space %s not found or not visible to this token", key)
	}
	return out.Results[0].ID, nil
}

func (c *Confluence) listPages(ctx context.Context, spaceID string) ([]cfPage, error) {
	var pages []cfPage
	next := "/wiki/api/v2/spaces/" + url.PathEscape(spaceID) + "/pages?limit=250&status=current"
	for next != "" {
		var out struct {
			Results []cfPage `json:"results"`
			Links   struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.get(ctx, next, &out); err != nil {
			return nil, err
		}
		pages = append(pages, out.Results...)
		next = out.Links.Next
	}
	return pages, nil
}

func (c *Confluence) fetchPage(ctx context.Context, spaceKey, pageID string) (Document, error) {
	var p cfPage
	if err := c.get(ctx, "/wiki/api/v2/pages/"+url.PathEscape(pageID)+"?body-format=storage", &p); err != nil {
		return Document{}, fmt.Errorf("fetch page %s: %w", pageID, err)
	}
	md, err := c.toMD.ConvertString(cleanStorage(p.Body.Storage.Value))
	if err != nil {
		return Document{}, fmt.Errorf("convert page %s: %w", pageID, err)
	}
	return Document{
		Title:   p.Title,
		Content: md,
		URL:     c.site + "/wiki" + p.Links.WebUI,
		Source:  c.name,
		Metadata: map[string]string{
			"language":   "markdown",
			"space":      spaceKey,
			"version":    strconv.FormatInt(p.Version.Number, 10),
			"updated_at": p.Version.CreatedAt.Format(time.RFC3339),
		},
	}, nil
}

// get fetches a path relative to the site root; pagination "next" links come back in that form.
func (c *Confluence) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.site+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.email != "" {
		req.SetBasicAuth(c.email, c.token)
	} else {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

var (
	codeMacro = regexp.MustCompile(`(?s)<ac:structured-macro[^>]*ac:name="code"[^>]*>(.*?)</ac:structured-macro>`)
	codeLang  = regexp.MustCompile(`(?s)<ac:parameter ac:name="language">(.*?)</ac:parameter>`)
	acParam   = regexp.MustCompile(`(?s)<ac:parameter[^>]*>.*?</ac:parameter>`)
	cdata     = regexp.MustCompile(`(?s)<!\[CDATA\[(.*?)\]\]>`)
)

// cleanStorage rewrites Confluence storage-format markup the HTML parser can't handle: code
// macros become <pre> blocks, macro parameters are dropped (they would leak as text), and
// remaining CDATA sections are unwrapped.
func cleanStorage(s string) string {
	s = codeMacro.ReplaceAllStringFunc(s, func(m string) string {
		inner := codeMacro.FindStringSubmatch(m)[1]
		lang := ""
		if l := codeLang.FindStringSubmatch(inner); l != nil {
			lang = ` class="language-` + html.EscapeString(l[1]) + `"`
		}
		body := ""
		if b := cdata.FindStringSubmatch(inner); b != nil {
			body = b[1]
		}
		return "<pre><code" + lang + ">" + html.EscapeString(body) + "</code></pre>"
	})
	s = acParam.ReplaceAllString(s, "")
	return cdata.ReplaceAllStringFunc(s, func(m string) string {
		return html.EscapeString(cdata.FindStringSubmatch(m)[1])
	})
}
