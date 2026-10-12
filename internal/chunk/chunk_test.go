package chunk

import (
	"strings"
	"testing"

	"github.com/noahfan/local-rag/internal/source"
)

func doc(content, lang string) source.Document {
	return source.Document{ID: "s:f", Source: "s", Content: content, Metadata: map[string]string{"language": lang}}
}

func TestSplitMarkdownByHeading(t *testing.T) {
	content := "# A\n" + strings.Repeat("alpha words here\n", 20) +
		"```\n# not a heading\n```\n" +
		"## B\n" + strings.Repeat("beta words here\n", 20)
	chunks := Split(doc(content, "markdown"), 200)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[1].Content, "## B") {
		t.Errorf("second chunk should start at heading B, got %q", chunks[1].Content[:10])
	}
	if !strings.Contains(chunks[0].Content, "# not a heading") {
		t.Error("heading inside code fence must not split")
	}
	if chunks[1].StartLine != 25 {
		t.Errorf("StartLine = %d, want 25", chunks[1].StartLine)
	}
}

func TestSplitCodePacksSmallDeclarations(t *testing.T) {
	content := "package p\n\nfunc a() {\n\treturn\n}\n\nfunc b() {\n\treturn\n}\n"
	chunks := Split(doc(content, "go"), 400)
	if len(chunks) != 1 {
		t.Fatalf("small declarations should pack into one chunk, got %d", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 9 {
		t.Errorf("lines = %d-%d, want 1-9", chunks[0].StartLine, chunks[0].EndLine)
	}
}

func TestSplitCodeAtDeclarationBoundary(t *testing.T) {
	body := strings.Repeat("\tx := computeSomething()\n", 30)
	content := "func a() {\n" + body + "}\n\nfunc b() {\n" + body + "}\n"
	chunks := Split(doc(content, "go"), 300)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(chunks))
	}
	if !strings.HasPrefix(chunks[1].Content, "func b()") {
		t.Errorf("second chunk should start at func b, got %q", chunks[1].Content[:12])
	}
	if chunks[0].ID != "s:f#0" || chunks[1].ID != "s:f#1" {
		t.Errorf("unexpected ids %s %s", chunks[0].ID, chunks[1].ID)
	}
}

func TestSplitRespectsMaxTokens(t *testing.T) {
	content := "func big() {\n" + strings.Repeat("\tline := withSomeLongerContentHere()\n", 200) + "}\n"
	const max = 100
	chunks := Split(doc(content, "go"), max)
	if len(chunks) < 2 {
		t.Fatalf("oversized section should be hard-split, got %d chunks", len(chunks))
	}
	prevEnd := 0
	for _, c := range chunks {
		if tok := EstimateTokens(c.Content); tok > max {
			t.Errorf("chunk %s has %d tokens > %d", c.ID, tok, max)
		}
		if c.StartLine != prevEnd+1 {
			t.Errorf("chunk %s starts at %d, want %d (contiguous)", c.ID, c.StartLine, prevEnd+1)
		}
		prevEnd = c.EndLine
	}
}

func TestEstimateTokensCJK(t *testing.T) {
	if got := EstimateTokens("abcdefgh"); got != 2 {
		t.Errorf("ascii: got %d, want 2", got)
	}
	if got := EstimateTokens("中文"); got != 2 {
		t.Errorf("cjk: got %d, want 2", got)
	}
}
