package film

// faces_store.go — face clusters + per-face boxes (M9, face curation).
// Each face references an already-uploaded keyframe (screenshot_id) + a
// normalized box; the cropped thumbnail is rendered client-side. The whole
// set for a movie is replaced atomically on (re-)push — see doc/face-curation.md.

import (
	"context"
	"database/sql"

	"github.com/lib/pq"
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

// ── curation ops (P1) ───────────────────────────────────────────────────────

// refreshCluster recomputes face_count, and re-picks the representative face if
// the current rep's keyframe is no longer in the cluster.
func refreshCluster(ctx context.Context, tx *sql.Tx, clusterID string) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM faces WHERE cluster_id=$1`, clusterID).Scan(&n); err != nil {
		return err
	}
	var rep string
	if err := tx.QueryRowContext(ctx, `SELECT rep_screenshot_id FROM face_clusters WHERE id=$1`, clusterID).Scan(&rep); err != nil {
		return err
	}
	stillThere := false
	if rep != "" {
		_ = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM faces WHERE cluster_id=$1 AND screenshot_id=$2)`, clusterID, rep).Scan(&stillThere)
	}
	if stillThere {
		_, err := tx.ExecContext(ctx, `UPDATE face_clusters SET face_count=$2 WHERE id=$1`, clusterID, n)
		return err
	}
	// pick a new rep from the first remaining face (or clear when empty).
	var sid string
	var bx, by, bw, bh float64
	err := tx.QueryRowContext(ctx, `
		SELECT screenshot_id, box_x, box_y, box_w, box_h
		FROM faces WHERE cluster_id=$1 ORDER BY ts_ms NULLS LAST LIMIT 1`, clusterID).
		Scan(&sid, &bx, &by, &bw, &bh)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE face_clusters SET rep_screenshot_id=$2, rep_box_x=$3, rep_box_y=$4, rep_box_w=$5, rep_box_h=$6, face_count=$7
		WHERE id=$1`, clusterID, sid, bx, by, bw, bh, n)
	return err
}

// clusterExists guards that a cluster belongs to the movie + workspace.
func (p *Plugin) clusterExists(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, wsID, mediaID, cid string) (bool, error) {
	var ok bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM face_clusters WHERE id=$1 AND media_id=$2 AND workspace_id=$3)`, cid, mediaID, wsID).Scan(&ok)
	return ok, err
}

// mergeFaceClusters moves every face from `from` clusters into `into`, adopts a
// from-person when `into` has none, then deletes the emptied `from` clusters.
func (p *Plugin) mergeFaceClusters(ctx context.Context, wsID, mediaID, into string, from []string) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if ok, err := p.clusterExists(ctx, tx, wsID, mediaID, into); err != nil {
		return err
	} else if !ok {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE faces SET cluster_id=$1 WHERE media_id=$2 AND workspace_id=$3 AND cluster_id=ANY($4)`,
		into, mediaID, wsID, pq.Array(from)); err != nil {
		return err
	}
	var intoPerson sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT person_id FROM face_clusters WHERE id=$1`, into).Scan(&intoPerson); err != nil {
		return err
	}
	if !intoPerson.Valid {
		var fp sql.NullString
		_ = tx.QueryRowContext(ctx, `SELECT person_id FROM face_clusters WHERE id=ANY($1) AND person_id IS NOT NULL LIMIT 1`, pq.Array(from)).Scan(&fp)
		if fp.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE face_clusters SET person_id=$1 WHERE id=$2`, fp.String, into); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM face_clusters WHERE id=ANY($1) AND media_id=$2 AND workspace_id=$3`, pq.Array(from), mediaID, wsID); err != nil {
		return err
	}
	if err := refreshCluster(ctx, tx, into); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureUnassignedCluster returns the movie's "unassigned" bucket, creating it
// on demand. Faces removed from a person land here for re-sorting.
func ensureUnassignedCluster(ctx context.Context, tx *sql.Tx, wsID, mediaID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM face_clusters WHERE media_id=$1 AND workspace_id=$2 AND label='unassigned' AND source='manual' LIMIT 1`,
		mediaID, wsID).Scan(&id)
	if err == sql.ErrNoRows {
		id = newID("fc_")
		_, err = tx.ExecContext(ctx, `
			INSERT INTO face_clusters (id, workspace_id, media_id, label, source, face_count, created_at)
			VALUES ($1,$2,$3,'unassigned','manual',0, now())`, id, wsID, mediaID)
		return id, err
	}
	return id, err
}

// removeFacesFromCluster moves selected faces out of `cid` into the unassigned bucket.
func (p *Plugin) removeFacesFromCluster(ctx context.Context, wsID, mediaID, cid string, faceIDs []string) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if ok, err := p.clusterExists(ctx, tx, wsID, mediaID, cid); err != nil {
		return err
	} else if !ok {
		return sql.ErrNoRows
	}
	un, err := ensureUnassignedCluster(ctx, tx, wsID, mediaID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE faces SET cluster_id=$1 WHERE media_id=$2 AND workspace_id=$3 AND cluster_id=$4 AND id=ANY($5)`,
		un, mediaID, wsID, cid, pq.Array(faceIDs)); err != nil {
		return err
	}
	if err := refreshCluster(ctx, tx, cid); err != nil {
		return err
	}
	if err := refreshCluster(ctx, tx, un); err != nil {
		return err
	}
	return tx.Commit()
}

// splitFaceCluster moves selected faces into a brand-new cluster (correcting a
// mis-merge of two people). Returns the new cluster id.
func (p *Plugin) splitFaceCluster(ctx context.Context, wsID, mediaID, cid string, faceIDs []string) (string, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	if ok, err := p.clusterExists(ctx, tx, wsID, mediaID, cid); err != nil {
		return "", err
	} else if !ok {
		return "", sql.ErrNoRows
	}
	newCid := newID("fc_")
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO face_clusters (id, workspace_id, media_id, label, source, face_count, created_at)
		VALUES ($1,$2,$3,'','manual',0, now())`, newCid, wsID, mediaID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE faces SET cluster_id=$1 WHERE media_id=$2 AND workspace_id=$3 AND cluster_id=$4 AND id=ANY($5)`,
		newCid, mediaID, wsID, cid, pq.Array(faceIDs)); err != nil {
		return "", err
	}
	if err := refreshCluster(ctx, tx, cid); err != nil {
		return "", err
	}
	if err := refreshCluster(ctx, tx, newCid); err != nil {
		return "", err
	}
	return newCid, tx.Commit()
}

// assignCluster sets a cluster's person and adds the person to the movie cast.
func (p *Plugin) assignCluster(ctx context.Context, wsID, mediaID, cid, personID string) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	res, err := tx.ExecContext(ctx, `UPDATE face_clusters SET person_id=$1 WHERE id=$2 AND media_id=$3 AND workspace_id=$4`,
		personID, cid, mediaID, wsID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO media_people (media_id, person_id, role, character, ord)
		SELECT $1,$2,'actor','',0
		WHERE EXISTS (SELECT 1 FROM people WHERE id=$2 AND workspace_id=$3)
		ON CONFLICT (media_id, person_id, role) DO NOTHING`, mediaID, personID, wsID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// moveFacesToPerson moves selected faces into the cluster that represents
// personID in this movie (find-or-create), and adds the person to the cast.
// Used to pick the right faces out of the "unassigned" bucket and label them.
func (p *Plugin) moveFacesToPerson(ctx context.Context, wsID, mediaID, fromCid string, faceIDs []string, personID string) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var target string
	err = tx.QueryRowContext(ctx, `SELECT id FROM face_clusters WHERE media_id=$1 AND workspace_id=$2 AND person_id=$3 LIMIT 1`, mediaID, wsID, personID).Scan(&target)
	if err == sql.ErrNoRows {
		target = newID("fc_")
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO face_clusters (id, workspace_id, media_id, label, person_id, source, face_count, created_at)
			VALUES ($1,$2,$3,'',$4,'manual',0, now())`, target, wsID, mediaID, personID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE faces SET cluster_id=$1 WHERE media_id=$2 AND workspace_id=$3 AND id=ANY($4)`,
		target, mediaID, wsID, pq.Array(faceIDs)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO media_people (media_id, person_id, role, character, ord)
		SELECT $1,$2,'actor','',0 WHERE EXISTS (SELECT 1 FROM people WHERE id=$2 AND workspace_id=$3)
		ON CONFLICT (media_id, person_id, role) DO NOTHING`, mediaID, personID, wsID); err != nil {
		return err
	}
	if err := refreshCluster(ctx, tx, target); err != nil {
		return err
	}
	if fromCid != "" && fromCid != target {
		if err := refreshCluster(ctx, tx, fromCid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensurePerson resolves (or creates) a person by name in the workspace.
func (p *Plugin) ensurePerson(ctx context.Context, wsID, name string) (string, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck
	id, err := p.ensurePersonTx(ctx, tx, wsID, name)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}
