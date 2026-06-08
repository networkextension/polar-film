package film

// embed_store.go — the M4 vector layer: generate + persist embeddings
// (subtitle segments, movie-level) and run pgvector cosine search.
//
// Embedding is best-effort and decoupled from ingest: segments/movies are
// written first (and stay keyword-searchable), then embedded in a second
// pass. A backend hiccup leaves embedding NULL rather than failing the
// write — reindex backfills later. Cosine uses pgvector's `<=>`; vectors
// are L2-normalized on the way in so 1-distance is a clean similarity.

import (
	"context"
	"fmt"
	"strings"
)

const embedBatch = 64 // texts per backend round-trip

// embedSegments embeds all of a subtitle's segments that still have a NULL
// embedding and writes them back. Returns the count embedded. Safe to call
// repeatedly (it only touches NULL rows).
func (p *Plugin) embedSegments(ctx context.Context, wsID, subtitleID string) (int, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, text FROM subtitle_segments
		WHERE workspace_id=$1 AND subtitle_id=$2 AND embedding IS NULL
		ORDER BY idx`, wsID, subtitleID)
	if err != nil {
		return 0, err
	}
	var ids, texts []string
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return p.embedAndStoreSegments(ctx, wsID, ids, texts)
}

// embedPendingSegments backfills up to `limit` NULL-embedding segments
// across the whole workspace (used by reindex).
func (p *Plugin) embedPendingSegments(ctx context.Context, wsID string, limit int) (int, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, text FROM subtitle_segments
		WHERE workspace_id=$1 AND embedding IS NULL
		ORDER BY media_id, idx LIMIT $2`, wsID, limit)
	if err != nil {
		return 0, err
	}
	var ids, texts []string
	for rows.Next() {
		var id, text string
		if err := rows.Scan(&id, &text); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return p.embedAndStoreSegments(ctx, wsID, ids, texts)
}

func (p *Plugin) embedAndStoreSegments(ctx context.Context, wsID string, ids, texts []string) (int, error) {
	done := 0
	for start := 0; start < len(ids); start += embedBatch {
		end := start + embedBatch
		if end > len(ids) {
			end = len(ids)
		}
		vecs, err := p.embedder.Embed(ctx, texts[start:end])
		if err != nil {
			return done, err
		}
		for i, v := range vecs {
			if _, err := p.DB.ExecContext(ctx,
				`UPDATE subtitle_segments SET embedding=$3::vector WHERE workspace_id=$1 AND id=$2`,
				wsID, ids[start+i], vectorLiteral(v)); err != nil {
				return done, err
			}
			done++
		}
	}
	p.metrics.addEmbed("segment", done)
	return done, nil
}

// embedMovie builds a movie's source text (title + summary + cast + tags)
// and upserts its media_embeddings row. Used on create/update and reindex.
func (p *Plugin) embedMovie(ctx context.Context, wsID, mediaID string) error {
	src, err := p.movieSourceText(ctx, wsID, mediaID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(src) == "" {
		return nil
	}
	vecs, err := p.embedder.Embed(ctx, []string{src})
	if err != nil || len(vecs) != 1 {
		if err == nil {
			err = fmt.Errorf("embedMovie: empty result")
		}
		return err
	}
	_, err = p.DB.ExecContext(ctx, `
		INSERT INTO media_embeddings (media_id, workspace_id, embedding, source_text, updated_at)
		VALUES ($1,$2,$3::vector,$4, now())
		ON CONFLICT (media_id) DO UPDATE
		SET embedding=EXCLUDED.embedding, source_text=EXCLUDED.source_text, updated_at=now()`,
		mediaID, wsID, vectorLiteral(vecs[0]), src)
	if err == nil {
		p.metrics.addEmbed("movie", 1)
	}
	return err
}

// movieSourceText concatenates the human-meaningful fields of a movie into
// a single embeddable string. Title carries the most weight, then summary,
// then cast names and tag labels.
func (p *Plugin) movieSourceText(ctx context.Context, wsID, mediaID string) (string, error) {
	var title, summary string
	err := p.DB.QueryRowContext(ctx,
		`SELECT title, COALESCE(summary,'') FROM media_items WHERE workspace_id=$1 AND id=$2`,
		wsID, mediaID).Scan(&title, &summary)
	if err != nil {
		return "", err
	}
	parts := []string{title}
	if summary != "" {
		parts = append(parts, summary)
	}
	// cast names (media_people has no workspace_id; mediaID is already
	// workspace-verified by the title lookup above)
	if rows, err := p.DB.QueryContext(ctx, `
		SELECT pe.name FROM media_people mp JOIN people pe ON pe.id=mp.person_id
		WHERE mp.media_id=$1 ORDER BY mp.ord`, mediaID); err == nil {
		var names []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil && n != "" {
				names = append(names, n)
			}
		}
		rows.Close()
		if len(names) > 0 {
			parts = append(parts, strings.Join(names, " "))
		}
	}
	// tag labels (media_tags has no workspace_id)
	if rows, err := p.DB.QueryContext(ctx, `
		SELECT t.name FROM media_tags mt JOIN tags t ON t.id=mt.tag_id
		WHERE mt.media_id=$1`, mediaID); err == nil {
		var labels []string
		for rows.Next() {
			var l string
			if rows.Scan(&l) == nil && l != "" {
				labels = append(labels, l)
			}
		}
		rows.Close()
		if len(labels) > 0 {
			parts = append(parts, strings.Join(labels, " "))
		}
	}
	return strings.Join(parts, "\n"), nil
}

// embedPendingMovies backfills movies whose media_embeddings row is missing
// or stale-NULL (used by reindex). Returns count embedded.
func (p *Plugin) embedPendingMovies(ctx context.Context, wsID string, limit int) (int, error) {
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT m.id FROM media_items m
		LEFT JOIN media_embeddings me ON me.media_id=m.id
		WHERE m.workspace_id=$1 AND (me.media_id IS NULL OR me.embedding IS NULL)
		LIMIT $2`, wsID, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	done := 0
	for _, id := range ids {
		if err := p.embedMovie(ctx, wsID, id); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}

// semanticSearchSegments runs a cosine kNN over subtitle segment embeddings.
func (p *Plugin) semanticSearchSegments(ctx context.Context, wsID string, qvec []float32, mediaID string, limit int) ([]SearchHit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	lit := vectorLiteral(qvec)
	args := []any{wsID, lit, limit}
	where := `s.workspace_id=$1 AND s.embedding IS NOT NULL`
	if mediaID != "" {
		where += ` AND s.media_id=$4`
		args = append(args, mediaID)
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT s.id, s.media_id, m.title, s.start_ms, s.end_ms, s.text,
		       1 - (s.embedding <=> $2::vector) AS score
		FROM subtitle_segments s JOIN media_items m ON m.id = s.media_id
		WHERE `+where+`
		ORDER BY s.embedding <=> $2::vector LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		var score float64
		if err := rows.Scan(&h.SegmentID, &h.MediaID, &h.Title, &h.StartMs, &h.EndMs, &h.Text, &score); err != nil {
			return nil, err
		}
		h.Score = &score
		out = append(out, h)
	}
	return out, rows.Err()
}

// SimilarMovie is a "相似片" result.
type SimilarMovie struct {
	MediaID string  `json:"media_id"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
}

// similarMovies returns the nearest movies to mediaID by movie-level
// embedding cosine. Returns an empty slice if the target has no embedding.
func (p *Plugin) similarMovies(ctx context.Context, wsID, mediaID string, limit int) ([]SimilarMovie, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := p.DB.QueryContext(ctx, `
		WITH target AS (
			SELECT embedding FROM media_embeddings
			WHERE workspace_id=$1 AND media_id=$2 AND embedding IS NOT NULL
		)
		SELECT me.media_id, m.title, 1 - (me.embedding <=> t.embedding) AS score
		FROM media_embeddings me
		JOIN media_items m ON m.id = me.media_id
		CROSS JOIN target t
		WHERE me.workspace_id=$1 AND me.media_id <> $2 AND me.embedding IS NOT NULL
		ORDER BY me.embedding <=> t.embedding LIMIT $3`, wsID, mediaID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SimilarMovie{}
	for rows.Next() {
		var s SimilarMovie
		if err := rows.Scan(&s.MediaID, &s.Title, &s.Score); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
