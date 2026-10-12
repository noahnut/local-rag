package chunk

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/noahfan/local-rag/internal/source"
)

type Chunk struct {
	ID        string
	DocID     string
	Source    string
	Title     string
	URL       string
	Content   string
	Metadata  map[string]string
	StartLine int // 1-based, inclusive
	EndLine   int
}

// EstimateTokens approximates tokenizer output: ~4 ASCII chars per token, ~1 token per CJK/other rune.
func EstimateTokens(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

var mdHeading = regexp.MustCompile(`^#{1,6}\s`)

// Split cuts a document into chunks of at most maxTokens (estimated), preferring Markdown headings
// or top-level declarations (non-indented lines after a blank line) as cut points.
func Split(doc source.Document, maxTokens int) []Chunk {
	lines := strings.Split(doc.Content, "\n")
	lineTok := make([]int, len(lines))
	for i, l := range lines {
		lineTok[i] = EstimateTokens(l) + 1
	}

	var chunks []Chunk
	emit := func(s, e int) {
		for s < e && strings.TrimSpace(lines[s]) == "" {
			s++
		}
		for e > s && strings.TrimSpace(lines[e-1]) == "" {
			e--
		}
		if s == e {
			return
		}
		chunks = append(chunks, Chunk{
			ID:        fmt.Sprintf("%s#%d", doc.ID, len(chunks)),
			DocID:     doc.ID,
			Source:    doc.Source,
			Title:     doc.Title,
			URL:       doc.URL,
			Content:   strings.Join(lines[s:e], "\n"),
			Metadata:  doc.Metadata,
			StartLine: s + 1,
			EndLine:   e,
		})
	}
	tokens := func(s, e int) int {
		t := 0
		for i := s; i < e; i++ {
			t += lineTok[i]
		}
		return t
	}

	starts := sectionStarts(lines, doc.Metadata["language"] == "markdown")
	starts = append(starts, len(lines))
	packStart, packTok := 0, 0
	for k := 0; k < len(starts)-1; k++ {
		s, e := starts[k], starts[k+1]
		t := tokens(s, e)
		if packTok > 0 && packTok+t > maxTokens {
			emit(packStart, s)
			packStart, packTok = s, 0
		}
		if t > maxTokens {
			hardSplit(lineTok, s, e, maxTokens, emit)
			packStart, packTok = e, 0
			continue
		}
		packTok += t
	}
	emit(packStart, len(lines))
	return chunks
}

func hardSplit(lineTok []int, s, e, maxTokens int, emit func(int, int)) {
	start, t := s, 0
	for i := s; i < e; i++ {
		if t > 0 && t+lineTok[i] > maxTokens {
			emit(start, i)
			start, t = i, 0
		}
		t += lineTok[i]
	}
	emit(start, e)
}

func sectionStarts(lines []string, markdown bool) []int {
	starts := []int{0}
	inFence := false
	for i, l := range lines {
		if markdown {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				inFence = !inFence
			} else if !inFence && i > 0 && mdHeading.MatchString(l) {
				starts = append(starts, i)
			}
			continue
		}
		if i > 0 && strings.TrimSpace(lines[i-1]) == "" && isTopLevel(l) {
			starts = append(starts, i)
		}
	}
	return starts
}

func isTopLevel(l string) bool {
	if l == "" {
		return false
	}
	switch l[0] {
	case ' ', '\t', '}', ')', ']':
		return false
	}
	return true
}
