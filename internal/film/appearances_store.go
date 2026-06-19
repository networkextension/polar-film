package film

// appearances_store.go — per-person navigation (PF-13). A curated person's
// on-screen presence comes from two independent sources joined only by the
// people row: face appearances (faces → face_clusters.person_id) and 台词
// (subtitle_segments.person_id, named speakers only). See doc/face-curation.md.

import (
	"context"
	"database/sql"
)

// PersonFrame is one face appearance of a person on a keyframe.
type PersonFrame struct {
	ScreenshotID string `json:"screenshot_id"`
	TsMs         *int   `json:"ts_ms,omitempty"`
	Box          Box    `json:"box"`
}

// PersonLine is one台词 attributed to a person.
type PersonLine struct {
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Text    string `json:"text"`
}

// getPersonName returns the person's name (and whether it exists in the workspace).
func (p *Plugin) getPersonName(ctx context.Context, wsID, pid string) (string, bool, error) {
	var name string
	err := p.DB.QueryRowContext(ctx, `SELECT name FROM people WHERE id=$1 AND workspace_id=$2`, pid, wsID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return name, err == nil, err
}

// listPersonFrames returns every face of person pid in a movie, time-ordered.
func (p *Plugin) listPersonFrames(ctx context.Context, wsID, mediaID, pid string) ([]PersonFrame, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT f.screenshot_id, f.ts_ms, f.box_x, f.box_y, f.box_w, f.box_h
		FROM faces f JOIN face_clusters fc ON fc.id = f.cluster_id
		WHERE fc.media_id=$1 AND fc.workspace_id=$2 AND fc.person_id=$3
		ORDER BY f.ts_ms NULLS LAST`, mediaID, wsID, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonFrame{}
	for rows.Next() {
		var fr PersonFrame
		var ts sql.NullInt64
		if err := rows.Scan(&fr.ScreenshotID, &ts, &fr.Box.X, &fr.Box.Y, &fr.Box.W, &fr.Box.H); err != nil {
			return nil, err
		}
		if ts.Valid {
			v := int(ts.Int64)
			fr.TsMs = &v
		}
		out = append(out, fr)
	}
	return out, rows.Err()
}

// listPersonLines returns every台词 attributed to person pid in a movie.
func (p *Plugin) listPersonLines(ctx context.Context, wsID, mediaID, pid string) ([]PersonLine, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT start_ms, end_ms, text
		FROM subtitle_segments
		WHERE workspace_id=$1 AND media_id=$2 AND person_id=$3
		ORDER BY start_ms`, wsID, mediaID, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PersonLine{}
	for rows.Next() {
		var ln PersonLine
		if err := rows.Scan(&ln.StartMs, &ln.EndMs, &ln.Text); err != nil {
			return nil, err
		}
		out = append(out, ln)
	}
	return out, rows.Err()
}
