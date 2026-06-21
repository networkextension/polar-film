package film

// screenshots_store.go — screenshot rows (pointers into polar-assets; the
// image bytes live there, keyed by asset_id). embedding column is added in M4.

import (
	"context"
	"database/sql"
	"time"
)

type Screenshot struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	MediaID     string    `json:"media_id"`
	TsMs        *int      `json:"ts_ms,omitempty"`
	AssetID     string    `json:"asset_id"`
	Phash       string    `json:"phash,omitempty"`
	OcrText     string    `json:"ocr_text,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (p *Plugin) insertScreenshot(ctx context.Context, s Screenshot) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO screenshots (id, workspace_id, media_id, ts_ms, asset_id, phash, ocr_text, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())`,
		s.ID, s.WorkspaceID, s.MediaID, nullInt(s.TsMs), s.AssetID, s.Phash, s.OcrText)
	return err
}

// listScreenshotsPage returns one page of a movie's screenshots (ordered by
// timestamp) plus the total row count, so the UI can lazy-page through large
// galleries instead of pulling the whole set (which could be hundreds of rows /
// hundreds of KB) in one response.
func (p *Plugin) listScreenshotsPage(ctx context.Context, wsID, mediaID string, limit, offset int) ([]Screenshot, int, error) {
	var total int
	if err := p.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM screenshots WHERE workspace_id=$1 AND media_id=$2`, wsID, mediaID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, media_id, ts_ms, asset_id, phash, ocr_text, created_at
		FROM screenshots WHERE workspace_id=$1 AND media_id=$2
		ORDER BY ts_ms NULLS LAST, created_at
		LIMIT $3 OFFSET $4`, wsID, mediaID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Screenshot{}
	for rows.Next() {
		var s Screenshot
		var ts sql.NullInt64
		if err := rows.Scan(&s.ID, &s.WorkspaceID, &s.MediaID, &ts, &s.AssetID, &s.Phash, &s.OcrText, &s.CreatedAt); err != nil {
			return nil, 0, err
		}
		s.TsMs = scanIntPtr(ts)
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// getScreenshot returns one row (for resolving asset_id → signed URL).
func (p *Plugin) getScreenshot(ctx context.Context, wsID, id string) (Screenshot, error) {
	var s Screenshot
	var ts sql.NullInt64
	err := p.DB.QueryRowContext(ctx, `
		SELECT id, workspace_id, media_id, ts_ms, asset_id, phash, ocr_text, created_at
		FROM screenshots WHERE workspace_id=$1 AND id=$2`, wsID, id).
		Scan(&s.ID, &s.WorkspaceID, &s.MediaID, &ts, &s.AssetID, &s.Phash, &s.OcrText, &s.CreatedAt)
	if err != nil {
		return Screenshot{}, err
	}
	s.TsMs = scanIntPtr(ts)
	return s, nil
}

func (p *Plugin) deleteScreenshot(ctx context.Context, wsID, id string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM screenshots WHERE workspace_id=$1 AND id=$2`, wsID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
