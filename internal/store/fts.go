package store

import (
	"regexp"
	"strings"
	"unicode"
)

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// spaceCJK separates CJK runes so FTS5's unicode61 tokenizer indexes them one character at a time
// (it otherwise treats a whole unspaced sentence as one token). Queries then match CJK runs as phrases.
func spaceCJK(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isCJK(r) {
			b.WriteByte(' ')
			b.WriteRune(r)
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

var identRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// splitIdent breaks camelCase / PascalCase / snake_case identifiers into words:
// "parseHTTPConfig" -> [parse HTTP Config].
func splitIdent(w string) []string {
	var parts []string
	for _, seg := range strings.Split(w, "_") {
		rs := []rune(seg)
		start := 0
		for i := 1; i < len(rs); i++ {
			lowerToUpper := unicode.IsLower(rs[i-1]) && unicode.IsUpper(rs[i])
			acronymEnd := i+1 < len(rs) && unicode.IsUpper(rs[i-1]) && unicode.IsUpper(rs[i]) && unicode.IsLower(rs[i+1])
			digitEdge := unicode.IsDigit(rs[i-1]) != unicode.IsDigit(rs[i])
			if lowerToUpper || acronymEnd || digitEdge {
				parts = append(parts, string(rs[start:i]))
				start = i
			}
		}
		if start < len(rs) {
			parts = append(parts, string(rs[start:]))
		}
	}
	return parts
}

// identParts lists the sub-words of compound identifiers in text, so "config" can match "parseConfig".
func identParts(text string) string {
	seen := map[string]bool{}
	var out []string
	for _, w := range identRe.FindAllString(text, -1) {
		parts := splitIdent(w)
		if len(parts) < 2 {
			continue
		}
		for _, p := range parts {
			p = strings.ToLower(p)
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return strings.Join(out, " ")
}

// matchQuery turns free text into an FTS5 MATCH expression: each word, identifier sub-word (when
// splitIdents) and CJK bigram becomes a quoted term or phrase, OR-ed together so BM25 ranks by overlap.
func matchQuery(q string, splitIdents bool) string {
	seen := map[string]bool{}
	var terms []string
	add := func(t string) {
		t = strings.ToLower(t)
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, `"`+t+`"`)
		}
	}
	var word, cjk []rune
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		w := string(word)
		add(w)
		if parts := splitIdent(w); splitIdents && len(parts) > 1 {
			for _, p := range parts {
				add(p)
			}
		}
		word = word[:0]
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		// Overlapping bigram phrases: a whole sentence as one phrase would rarely match verbatim.
		if len(cjk) == 1 {
			add(string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			add(string(cjk[i]) + " " + string(cjk[i+1]))
		}
		cjk = cjk[:0]
	}
	for _, r := range q {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return strings.Join(terms, " OR ")
}
