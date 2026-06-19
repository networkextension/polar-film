package film

// faces_store.go — face clusters + per-face boxes (M9, face curation).
// Each face references an already-uploaded keyframe (screenshot_id) + a
// normalized box; the cropped thumbnail is rendered client-side. The whole
// set for a movie is replaced atomically on (re-)push — see doc/face-curation.md.

import (
	"context"
	"database/sql"
)

// Box is a normalized face box (top-left origin, [0,1]).
type Box struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

type FaceCluster struct {
	ID              string  `json:"id"`
	WorkspaceID     string  `json:"workspace_id"`
	MediaID         string  `json:"media_id"`
	Label           string  `json:"label"`
	PersonID        string  `json:"person_id,omitempty"`
	PersonName      string  `json:"person_name,omitempty"` // joined from people
	RepScreenshotID string  `json:"rep_screenshot_id"`
	RepBox          Box     `json:"rep_box"`
	FaceCount       int     `json:"face_count"`
	Source          string  `json:"source"`
	Conf            float64 `json:"conf"`
}

type Face struct {
	ID           string `json:"id"`
	ClusterID    string `json:"cluster_id"`
	ScreenshotID string `json:"screenshot_id"`
	TsMs         *int   `json:"ts_ms,omitempty"`
	Box          Box    `json:"box"`
	Quality      float64 `json:"quality"`
}

// replaceMovieFaces atomically swaps the whole face set for a movie: delete
// the existing clusters (faces cascade) then bulk-insert the new ones. Makes
// re-push idempotent (the latest analyze wins). cluster.ID and each face's
// ClusterID must already be minted + consistent by the caller.
func (p *Plugin) replaceMovieFaces(ctx context.Context, wsID, mediaID string, clusters []FaceCluster, faces []Face) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, `DELETE FROM face_clusters WHERE media_id=$1 AND workspace_id=$2`, mediaID, wsID); err != nil {
		return err
	}
	for _, c := range clusters {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO face_clusters
			  (id, workspace_id, media_id, label, rep_screenshot_id, rep_box_x, rep_box_y, rep_box_w, rep_box_h, face_count, source, conf, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())`,
			c.ID, wsID, mediaID, c.Label, c.RepScreenshotID,
			c.RepBox.X, c.RepBox.Y, c.RepBox.W, c.RepBox.H, c.FaceCount, nz(c.Source, "filmscan"), c.Conf); err != nil {
			return err
		}
	}
	for _, f := range faces {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO faces
			  (id, workspace_id, media_id, cluster_id, screenshot_id, ts_ms, box_x, box_y, box_w, box_h, quality, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())`,
			f.ID, wsID, mediaID, f.ClusterID, f.ScreenshotID, nullInt(f.TsMs),
			f.Box.X, f.Box.Y, f.Box.W, f.Box.H, f.Quality); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// listFaceClusters returns a movie's clusters (busiest first), joining the
// resolved person name when assigned.
func (p *Plugin) listFaceClusters(ctx context.Context, wsID, mediaID string) ([]FaceCluster, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT fc.id, fc.workspace_id, fc.media_id, fc.label, COALESCE(fc.person_id,''),
		       COALESCE(pe.name,''), fc.rep_screenshot_id,
		       fc.rep_box_x, fc.rep_box_y, fc.rep_box_w, fc.rep_box_h,
		       fc.face_count, fc.source, fc.conf
		FROM face_clusters fc
		LEFT JOIN people pe ON pe.id = fc.person_id
		WHERE fc.workspace_id=$1 AND fc.media_id=$2
		ORDER BY fc.face_count DESC, fc.label`, wsID, mediaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FaceCluster{}
	for rows.Next() {
		var c FaceCluster
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.MediaID, &c.Label, &c.PersonID,
			&c.PersonName, &c.RepScreenshotID,
			&c.RepBox.X, &c.RepBox.Y, &c.RepBox.W, &c.RepBox.H,
			&c.FaceCount, &c.Source, &c.Conf); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// listClusterFaces returns the faces in one cluster (for the curation grid).
func (p *Plugin) listClusterFaces(ctx context.Context, wsID, clusterID string) ([]Face, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, cluster_id, screenshot_id, ts_ms, box_x, box_y, box_w, box_h, quality
		FROM faces WHERE workspace_id=$1 AND cluster_id=$2
		ORDER BY ts_ms NULLS LAST`, wsID, clusterID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Face{}
	for rows.Next() {
		var f Face
		var ts sql.NullInt64
		if err := rows.Scan(&f.ID, &f.ClusterID, &f.ScreenshotID, &ts,
			&f.Box.X, &f.Box.Y, &f.Box.W, &f.Box.H, &f.Quality); err != nil {
			return nil, err
		}
		if ts.Valid {
			v := int(ts.Int64)
			f.TsMs = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func nz(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
