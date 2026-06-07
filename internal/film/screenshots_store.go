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

func (p *Plugin) listScreenshots(ctx context.Context, wsID, mediaID string) ([]Screenshot, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, media_id, ts_ms, asset_id, phash, ocr_text, created_at
		FROM screenshots WHERE workspace_id=$1 AND media_id=$2 ORDER BY ts_ms NULLS LAST, created_at`, wsID, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Screenshot{}
	for rows.Next() {
		var s Screenshot
		var ts sql.NullInt64
		if err := rows.Scan(&s.ID, &s.WorkspaceID, &s.MediaID, &ts, &s.AssetID, &s.Phash, &s.OcrText, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.TsMs = scanIntPtr(ts)
		out = append(out, s)
	}
	return out, rows.Err()
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
