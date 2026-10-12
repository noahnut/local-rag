package search

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/embed"
	"github.com/noahfan/local-rag/internal/store"
)

// Store is what the searcher needs from storage; a Qdrant backend would implement the same methods.
type Store interface {
	Keyword(ctx context.Context, query, source string, limit int, exact bool) ([]string, error)
	Vectors(ctx context.Context) ([]store.Vector, error)
	Meta(ctx context.Context, key string) (string, error)
	Get(ctx context.Context, id string) (chunk.Chunk, error)
}

const (
	candidates = 30
	rrfK       = 60
)

type Options struct {
	TopK      int
	MaxTokens int
	Source    string
}

type Result struct {
	Chunk chunk.Chunk
	Score float64
}

type Searcher struct {
	store       Store
	embedder    embed.Embedder
	queryPrefix string

	mu      sync.Mutex
	version string
	vecs    []store.Vector
}

func New(st Store, emb embed.Embedder, queryPrefix string) *Searcher {
	return &Searcher{store: st, embedder: emb, queryPrefix: queryPrefix}
}

func (s *Searcher) Search(ctx context.Context, query string, o Options) ([]Result, error) {
	ident := looksLikeIdentifier(query)
	var kw []string
	var err error
	if ident {
		// Exact symbol hits first; sub-word matches would otherwise crowd out the definition.
		kw, err = s.store.Keyword(ctx, query, o.Source, candidates, true)
	}
	if err == nil && len(kw) == 0 {
		kw, err = s.store.Keyword(ctx, query, o.Source, candidates, false)
	}
	if err != nil {
		return nil, err
	}
	vec, err := s.vectorSearch(ctx, query, o.Source)
	if err != nil {
		// Keep answering with BM25 when the embedding backend (e.g. Ollama) is down.
		log.Printf("vector search unavailable, using keyword results only: %v", err)
	}
	kwWeight := 1.0
	if ident {
		// Embeddings barely understand symbol names, so exact BM25 hits should dominate.
		kwWeight = 2
	}
	return s.pack(ctx, rrf(ranked{kw, kwWeight}, ranked{vec, 1}), o)
}

var identQuery = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// looksLikeIdentifier reports single-token queries such as parseConfig, MAX_SIZE or pkg.Func.
func looksLikeIdentifier(q string) bool {
	q = strings.TrimSpace(q)
	if !identQuery.MatchString(q) {
		return false
	}
	if strings.ContainsAny(q, "_.") {
		return true
	}
	for i := 1; i < len(q); i++ {
		if q[i] >= 'A' && q[i] <= 'Z' {
			return true
		}
	}
	return false
}

func (s *Searcher) vectorSearch(ctx context.Context, query, source string) ([]string, error) {
	qv, err := s.embedder.Embed(ctx, []string{s.queryPrefix + query})
	if err != nil {
		return nil, err
	}
	q := normalize(qv[0])
	vecs, err := s.loadVectors(ctx)
	if err != nil {
		return nil, err
	}
	type hit struct {
		id    string
		score float32
	}
	var hits []hit
	for _, v := range vecs {
		if source != "" && v.Source != source {
			continue
		}
		if len(v.Vec) != len(q) {
			return nil, fmt.Errorf("index has %d-dim vectors but query has %d; run `local-rag index --rebuild`", len(v.Vec), len(q))
		}
		var dot float32
		for i := range q {
			dot += q[i] * v.Vec[i]
		}
		hits = append(hits, hit{v.ID, dot})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	ids := make([]string, 0, candidates)
	for i := 0; i < len(hits) && i < candidates; i++ {
		ids = append(ids, hits[i].id)
	}
	return ids, nil
}

// loadVectors caches normalized vectors in memory, reloading only when the indexer bumps "version".
func (s *Searcher) loadVectors(ctx context.Context) ([]store.Vector, error) {
	version, err := s.store.Meta(ctx, "version")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vecs != nil && version == s.version {
		return s.vecs, nil
	}
	vecs, err := s.store.Vectors(ctx)
	if err != nil {
		return nil, err
	}
	for i := range vecs {
		vecs[i].Vec = normalize(vecs[i].Vec)
	}
	s.vecs, s.version = vecs, version
	return vecs, nil
}

type scored struct {
	id    string
	score float64
}

type ranked struct {
	ids    []string
	weight float64
}

// rrf merges ranked id lists with weighted Reciprocal Rank Fusion: score = Σ weight/(k + rank).
func rrf(lists ...ranked) []scored {
	scores := map[string]float64{}
	for _, l := range lists {
		for rank, id := range l.ids {
			scores[id] += l.weight / float64(rrfK+rank+1)
		}
	}
	out := make([]scored, 0, len(scores))
	for id, sc := range scores {
		out = append(out, scored{id, sc})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].id < out[j].id
	})
	return out
}

// pack takes fused results in order until TopK or the token budget is reached; the top hit is
// always included. Chunks that would overflow the budget are skipped so smaller ones can still fit.
func (s *Searcher) pack(ctx context.Context, fused []scored, o Options) ([]Result, error) {
	var out []Result
	used := 0
	for _, f := range fused {
		if len(out) >= o.TopK {
			break
		}
		c, err := s.store.Get(ctx, f.id)
		if err != nil {
			return nil, err
		}
		t := chunk.EstimateTokens(c.Content)
		if len(out) > 0 && used+t > o.MaxTokens {
			continue
		}
		used += t
		out = append(out, Result{Chunk: c, Score: f.score})
	}
	return out, nil
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	n := float32(math.Sqrt(sum))
	if n == 0 {
		return v
	}
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = f / n
	}
	return out
}
