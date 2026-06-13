package film

// subtitles_store.go — subtitles + subtitle_segments persistence and the
// keyword (台词) search. Vector search arrives in M4.

import (
	"context"
	"time"
)

type Subtitle struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	MediaID     string    `json:"media_id"`
	Lang        string    `json:"lang"`
	Format      string    `json:"format"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
}

type Segment struct {
	ID      string `json:"id"`
	Idx     int    `json:"idx"`
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
}

// SearchHit is one台词 match (segment joined with its media title).
type SearchHit struct {
	SegmentID string `json:"segment_id"`
	MediaID   string `json:"media_id"`
	Title     string `json:"title"`
	StartMs   int    `json:"start_ms"`
	EndMs     int    `json:"end_ms"`
	Text      string `json:"text"`
	// Score is the cosine similarity (0..1) for semantic hits; nil (and
	// omitted) for keyword (台词 substring) results. A pointer so a real
	// 0.0 similarity still serializes.
	Score *float64 `json:"score,omitempty"`
}

// insertSubtitleWithSegments writes the subtitle + all its segments in one
// transaction (the cache mirrors the file atomically).
func (p *Plugin) insertSubtitleWithSegments(ctx context.Context, s Subtitle, cues []parsedCue) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — no-op after Commit

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO subtitles (id, workspace_id, media_id, lang, format, source, created_at)
		VALUES ($1,$2,$3,$4,$5,$6, now())`,
		s.ID, s.WorkspaceID, s.MediaID, s.Lang, s.Format, s.Source); err != nil {
		return err
	}
	for _, cu := range cues {
		// speaker_key carries filmscan's "[Name]"/"spkN" attribution when the
		// cue had one; NULL (via NULLIF) for ordinary subtitles.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO subtitle_segments (id, workspace_id, subtitle_id, media_id, idx, start_ms, end_ms, text, speaker_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8, NULLIF($9,''))`,
			newID("seg_"), s.WorkspaceID, s.ID, s.MediaID, cu.Idx, cu.StartMs, cu.EndMs, cu.Text, cu.Speaker); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *Plugin) listSubtitles(ctx context.Context, wsID, mediaID string) ([]Subtitle, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, media_id, lang, format, source, created_at
		FROM subtitles WHERE workspace_id=$1 AND media_id=$2 ORDER BY created_at`, wsID, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtitle{}
	for rows.Next() {
		var s Subtitle
		if err := rows.Scan(&s.ID, &s.WorkspaceID, &s.MediaID, &s.Lang, &s.Format, &s.Source, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listSegments returns a subtitle's segments (workspace-scoped via the join).
func (p *Plugin) listSegments(ctx context.Context, wsID, subtitleID string) ([]Segment, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, idx, start_ms, end_ms, text FROM subtitle_segments
		WHERE workspace_id=$1 AND subtitle_id=$2 ORDER BY start_ms`, wsID, subtitleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Segment{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.ID, &s.Idx, &s.StartMs, &s.EndMs, &s.Text); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *Plugin) subtitleExists(ctx context.Context, wsID, subtitleID string) bool {
	var one int
	return p.DB.QueryRowContext(ctx, `SELECT 1 FROM subtitles WHERE workspace_id=$1 AND id=$2`, wsID, subtitleID).Scan(&one) == nil
}

func (p *Plugin) deleteSubtitle(ctx context.Context, wsID, id string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM subtitles WHERE workspace_id=$1 AND id=$2`, wsID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// searchSegments does a case-insensitive substring (台词) search across all
// of the workspace's subtitle segments. Optional mediaID narrows to one film.
func (p *Plugin) searchSegments(ctx context.Context, wsID, q, mediaID string, limit int) ([]SearchHit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	pattern := "%" + q + "%"
	args := []any{wsID, pattern, limit}
	where := `s.workspace_id=$1 AND s.text ILIKE $2`
	if mediaID != "" {
		where += ` AND s.media_id=$4`
		args = append(args, mediaID)
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT s.id, s.media_id, m.title, s.start_ms, s.end_ms, s.text
		FROM subtitle_segments s JOIN media_items m ON m.id = s.media_id
		WHERE `+where+`
		ORDER BY m.title, s.start_ms LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SearchHit{}
	for rows.Next() {
		var h SearchHit
		if err := rows.Scan(&h.SegmentID, &h.MediaID, &h.Title, &h.StartMs, &h.EndMs, &h.Text); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
