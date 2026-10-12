package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"

	"github.com/noahfan/local-rag/internal/chunk"
	src "github.com/noahfan/local-rag/internal/source"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS chunks (
	id         TEXT PRIMARY KEY,
	doc_id     TEXT NOT NULL,
	source     TEXT NOT NULL,
	title      TEXT NOT NULL,
	url        TEXT NOT NULL,
	content    TEXT NOT NULL,
	metadata   TEXT NOT NULL,
	hash       TEXT NOT NULL,
	start_line INTEGER NOT NULL,
	end_line   INTEGER NOT NULL,
	embedding  BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS chunks_source ON chunks(source);
CREATE TABLE IF NOT EXISTS files (
	source   TEXT NOT NULL,
	doc_id   TEXT NOT NULL,
	mod_time INTEGER NOT NULL,
	size     INTEGER NOT NULL,
	PRIMARY KEY (source, doc_id)
);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(title, content, idents, tokenize='unicode61');
`

type Record struct {
	chunk.Chunk
	Hash      string
	Embedding []float32
}

type Vector struct {
	ID     string
	Source string
	Vec    []float32
}

type SourceStat struct {
	Source string
	Chunks int
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

type ChunkRef struct {
	DocID string
	Hash  string
}

// ChunkRefs returns chunk id -> (doc id, content hash) for one source, used to skip unchanged chunks.
func (s *Store) ChunkRefs(ctx context.Context, source string) (map[string]ChunkRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, doc_id, hash FROM chunks WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ChunkRef{}
	for rows.Next() {
		var id string
		var r ChunkRef
		if err := rows.Scan(&id, &r.DocID, &r.Hash); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

func (s *Store) FileStamps(ctx context.Context, source string) (map[string]src.FileStamp, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT doc_id, mod_time, size FROM files WHERE source = ?`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]src.FileStamp{}
	for rows.Next() {
		var id string
		var fs src.FileStamp
		if err := rows.Scan(&id, &fs.ModTime, &fs.Size); err != nil {
			return nil, err
		}
		out[id] = fs
	}
	return out, rows.Err()
}

func (s *Store) SetFileStamps(ctx context.Context, source string, set map[string]src.FileStamp, del []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, fs := range set {
		if _, err := tx.ExecContext(ctx, `INSERT INTO files(source, doc_id, mod_time, size) VALUES(?, ?, ?, ?)
			ON CONFLICT(source, doc_id) DO UPDATE SET mod_time = excluded.mod_time, size = excluded.size`,
			source, id, fs.ModTime, fs.Size); err != nil {
			return err
		}
	}
	for _, id := range del {
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE source = ? AND doc_id = ?`, source, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TryLease takes a named lock shared by every process using this database, so concurrent servers
// don't embed the same changes twice. It expires after ttl in case the holder dies.
func (s *Store) TryLease(ctx context.Context, key string, ttl time.Duration) (release func(), ok bool, err error) {
	now := time.Now().UnixNano()
	until := strconv.FormatInt(now+int64(ttl), 10)
	res, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value WHERE CAST(meta.value AS INTEGER) < ?`, key, until, now)
	if err != nil {
		return nil, false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return nil, false, err
	}
	release = func() {
		s.db.Exec(`UPDATE meta SET value = '0' WHERE key = ? AND value = ?`, key, until)
	}
	return release, true, nil
}

func (s *Store) Upsert(ctx context.Context, recs []Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range recs {
		if err := deleteChunk(ctx, tx, r.ID); err != nil {
			return err
		}
		meta, err := json.Marshal(r.Metadata)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO chunks(id, doc_id, source, title, url, content, metadata, hash, start_line, end_line, embedding)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.DocID, r.Source, r.Title, r.URL, r.Content, string(meta), r.Hash, r.StartLine, r.EndLine, encodeVec(r.Embedding))
		if err != nil {
			return err
		}
		rowid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(rowid, title, content, idents) VALUES(?, ?, ?, ?)`,
			rowid, spaceCJK(r.Title), spaceCJK(r.Content), identParts(r.Title+"\n"+r.Content)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Delete(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if err := deleteChunk(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func deleteChunk(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE rowid = (SELECT rowid FROM chunks WHERE id = ?)`, id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id = ?`, id)
	return err
}

func (s *Store) Clear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chunks; DELETE FROM chunks_fts; DELETE FROM files;`)
	return err
}

// Keyword returns chunk ids ranked by BM25. An empty source searches all sources. With exact,
// identifiers only match as whole tokens (parseConfig does not match "parse" or "config").
func (s *Store) Keyword(ctx context.Context, query, source string, limit int, exact bool) ([]string, error) {
	match := matchQuery(query, !exact)
	if match == "" {
		return nil, nil
	}
	// bm25 column weights: title (file path / note title), content, identifier sub-words.
	rows, err := s.db.QueryContext(ctx, `SELECT c.id FROM chunks_fts f JOIN chunks c ON c.rowid = f.rowid
		WHERE chunks_fts MATCH ? AND (? = '' OR c.source = ?)
		ORDER BY bm25(chunks_fts, 3.0, 1.0, 0.5) LIMIT ?`, match, source, source, limit)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) Vectors(ctx context.Context) ([]Vector, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source, embedding FROM chunks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vector
	for rows.Next() {
		var v Vector
		var blob []byte
		if err := rows.Scan(&v.ID, &v.Source, &blob); err != nil {
			return nil, err
		}
		v.Vec = decodeVec(blob)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (chunk.Chunk, error) {
	var c chunk.Chunk
	var meta string
	err := s.db.QueryRowContext(ctx, `SELECT id, doc_id, source, title, url, content, metadata, start_line, end_line FROM chunks WHERE id = ?`, id).
		Scan(&c.ID, &c.DocID, &c.Source, &c.Title, &c.URL, &c.Content, &meta, &c.StartLine, &c.EndLine)
	if err == sql.ErrNoRows {
		return c, fmt.Errorf("chunk %q not found", id)
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(meta), &c.Metadata)
	return c, err
}

func (s *Store) Stats(ctx context.Context) ([]SourceStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, COUNT(*) FROM chunks GROUP BY source ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceStat
	for rows.Next() {
		var st SourceStat
		if err := rows.Scan(&st.Source, &st.Chunks); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
