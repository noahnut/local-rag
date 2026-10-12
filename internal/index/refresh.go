package index

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/noahfan/local-rag/internal/source"
)

const leaseKey = "refresh_lease"

// Refresher keeps the index current from inside the MCP server: before each search it applies
// small changes inline and hands large ones to a background goroutine.
type Refresher struct {
	Indexer   *Indexer
	Sources   []source.DocumentSource
	Model     string        // must match the index's embed_model, otherwise vectors would mix
	Interval  time.Duration // minimum time between checks
	SyncLimit int           // pending chunks applied inline; more go to the background
	LeaseTTL  time.Duration

	mu        sync.Mutex // held for the whole refresh, including a background apply
	lastCheck time.Time
	bgPending atomic.Int64
}

// Refresh returns a note for the user when results may be stale or a source needs attention.
func (r *Refresher) Refresh(ctx context.Context) string {
	if !r.mu.TryLock() {
		if n := r.bgPending.Load(); n > 0 {
			return fmt.Sprintf("Index update in progress (%d chunks pending); results may be stale.", n)
		}
		return ""
	}
	unlock := true
	defer func() {
		if unlock {
			r.mu.Unlock()
		}
	}()
	if time.Since(r.lastCheck) < r.Interval {
		return ""
	}
	r.lastCheck = time.Now()

	st := r.Indexer.Store
	model, err := st.Meta(ctx, "embed_model")
	if err != nil {
		log.Printf("refresh: %v", err)
		return ""
	}
	if model != r.Model {
		if model == "" {
			return "Index is empty; run `local-rag index` first."
		}
		return fmt.Sprintf("Index was built with %s but config uses %s; run `local-rag index --rebuild`.", model, r.Model)
	}

	release, ok, err := st.TryLease(ctx, leaseKey, r.LeaseTTL)
	if err != nil || !ok {
		if err != nil {
			log.Printf("refresh: %v", err)
		}
		return "" // another local-rag process is updating the index
	}

	var notes []string
	var plans []*Plan
	total := 0
	for _, src := range r.Sources {
		p, err := r.Indexer.Plan(ctx, src)
		if err != nil {
			log.Printf("refresh %s: %v", src.Name(), err)
			continue
		}
		if p.Fresh && p.Pending() > r.SyncLimit {
			notes = append(notes, fmt.Sprintf("Source %s is not indexed yet; run `local-rag index`.", p.Source))
			continue
		}
		plans = append(plans, p)
		total += p.Pending()
	}

	if total <= r.SyncLimit {
		r.apply(ctx, plans)
		release()
		return strings.Join(notes, " ")
	}

	unlock = false
	r.bgPending.Store(int64(total))
	go func() {
		defer r.mu.Unlock()
		defer release()
		defer r.bgPending.Store(0)
		r.apply(context.Background(), plans)
	}()
	notes = append(notes, fmt.Sprintf("Index update started in the background (%d chunks); results may be stale.", total))
	return strings.Join(notes, " ")
}

func (r *Refresher) apply(ctx context.Context, plans []*Plan) {
	for _, p := range plans {
		stats, err := r.Indexer.Apply(ctx, p)
		if err != nil {
			log.Printf("refresh %s: %v", p.Source, err)
			continue
		}
		if stats.Embedded > 0 || stats.Deleted > 0 {
			log.Printf("refresh %s: %d docs changed, %d embedded, %d deleted", p.Source, stats.Documents, stats.Embedded, stats.Deleted)
		}
	}
}
