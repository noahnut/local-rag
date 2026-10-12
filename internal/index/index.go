package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"

	"github.com/noahfan/local-rag/internal/chunk"
	"github.com/noahfan/local-rag/internal/embed"
	"github.com/noahfan/local-rag/internal/source"
	"github.com/noahfan/local-rag/internal/store"
)

type Indexer struct {
	Store          *store.Store
	Embedder       embed.Embedder
	DocumentPrefix string
	MaxTokens      int
	BatchSize      int
}

type Stats struct {
	Documents, Embedded, Deleted int
}

// Plan is the diff between a source and the index; computing it embeds nothing.
type Plan struct {
	Source    string
	Fresh     bool // nothing from this source has been indexed yet
	Documents int  // documents re-read
	pending   []store.Record
	stale     []string
	stamps    map[string]source.FileStamp
	gone      []string
}

func (p *Plan) Pending() int { return len(p.pending) }

func (ix *Indexer) Plan(ctx context.Context, src source.DocumentSource) (*Plan, error) {
	refs, err := ix.Store.ChunkRefs(ctx, src.Name())
	if err != nil {
		return nil, err
	}
	p := &Plan{Source: src.Name(), stamps: map[string]source.FileStamp{}}

	var docs []source.Document
	// inScope reports whether a document's existing chunks may be replaced or deleted.
	inScope := func(string) bool { return true }
	if inc, ok := src.(source.Incremental); ok {
		known, err := ix.Store.FileStamps(ctx, src.Name())
		if err != nil {
			return nil, err
		}
		changed, present, err := inc.FetchChanged(ctx, known)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", src.Name(), err)
		}
		docs = changed
		touched := map[string]bool{}
		for _, d := range changed {
			touched[d.ID] = true
			p.stamps[d.ID] = present[d.ID]
		}
		for id := range known {
			if _, ok := present[id]; !ok {
				touched[id] = true
				p.gone = append(p.gone, id)
			}
		}
		inScope = func(docID string) bool { return touched[docID] }
		p.Fresh = len(known) == 0 && len(refs) == 0
	} else {
		if docs, err = src.FetchDocuments(ctx); err != nil {
			return nil, fmt.Errorf("fetch %s: %w", src.Name(), err)
		}
		p.Fresh = len(refs) == 0
	}
	p.Documents = len(docs)

	seen := map[string]bool{}
	for _, d := range docs {
		for _, c := range chunk.Split(d, ix.MaxTokens) {
			seen[c.ID] = true
			h := hashChunk(c)
			if refs[c.ID].Hash == h {
				continue
			}
			p.pending = append(p.pending, store.Record{Chunk: c, Hash: h})
		}
	}
	for id, ref := range refs {
		if inScope(ref.DocID) && !seen[id] {
			p.stale = append(p.stale, id)
		}
	}
	return p, nil
}

// Apply embeds and writes a plan. File stamps are saved last, so an interrupted apply is redone
// next time; chunks already written are then skipped by their hash.
func (ix *Indexer) Apply(ctx context.Context, p *Plan) (Stats, error) {
	st := Stats{Documents: p.Documents}
	for start := 0; start < len(p.pending); start += ix.BatchSize {
		batch := p.pending[start:min(start+ix.BatchSize, len(p.pending))]
		texts := make([]string, len(batch))
		for i, r := range batch {
			texts[i] = ix.DocumentPrefix + r.Title + "\n" + r.Content
		}
		vecs, err := ix.Embedder.Embed(ctx, texts)
		if err != nil {
			return st, err
		}
		for i := range batch {
			batch[i].Embedding = vecs[i]
		}
		if err := ix.Store.Upsert(ctx, batch); err != nil {
			return st, err
		}
		st.Embedded += len(batch)
	}
	if err := ix.Store.Delete(ctx, p.stale); err != nil {
		return st, err
	}
	st.Deleted = len(p.stale)
	if err := ix.Store.SetFileStamps(ctx, p.Source, p.stamps, p.gone); err != nil {
		return st, err
	}
	if st.Embedded > 0 || st.Deleted > 0 {
		return st, ix.Store.SetMeta(ctx, "version", strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	return st, nil
}

func (ix *Indexer) Index(ctx context.Context, src source.DocumentSource) (Stats, error) {
	p, err := ix.Plan(ctx, src)
	if err != nil {
		return Stats{}, err
	}
	return ix.Apply(ctx, p)
}

// hashChunk covers line numbers too, so a chunk that only moved still gets its location updated.
func hashChunk(c chunk.Chunk) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%s", c.Title, c.URL, c.StartLine, c.EndLine, c.Content)
	return hex.EncodeToString(h.Sum(nil))
}
