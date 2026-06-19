package film

// face_search_store.go — face re-identification queries over the per-face
// Vision feature-print stored in pgvector (PF-14). Three reads, all exact
// cosine NN scans (`<=>`, no index — face counts are modest):
//   1. cluster-centroid pairs   → merge suggestions (疑似同人)
//   2. within-movie similar face → assist manual grouping
//   3. cross-film person search  → "还演过哪些片"
// See doc/face-curation.md. Embeddings are whole-crop feature-prints, so these
// are assists (conservative thresholds), not authoritative identity.

import (
	"context"
	"database/sql"
)

// SuggestionPair is one疑似同人 cluster pair (raw — enriched in the handler).
type SuggestionPair struct {
	A    string  `json:"cluster_a"`
	B    string  `json:"cluster_b"`
	Dist float64 `json:"distance"`
}

// faceClusterCentroidPairs returns within-movie cluster pairs whose embedding
// centroids are within maxDist (cosine), closest first. The unassigned bucket
// is excluded (its centroid mixes people). Caller enriches with cluster meta.
func (p *Plugin) faceClusterCentroidPairs(ctx context.Context, wsID, mediaID string, maxDist float64, limit int) ([]SuggestionPair, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := p.DB.QueryContext(ctx, `
		WITH cent AS (
			SELECT f.cluster_id, avg(f.embedding) AS c
			FROM faces f
			JOIN face_clusters fc ON fc.id = f.cluster_id
			WHERE f.media_id=$1 AND f.workspace_id=$2 AND f.embedding IS NOT NULL
			  AND fc.label <> 'unassigned'
			GROUP BY f.cluster_id
		)
		SELECT a.cluster_id, b.cluster_id, (a.c <=> b.c) AS dist
		FROM cent a JOIN cent b ON a.cluster_id < b.cluster_id
		WHERE (a.c <=> b.c) <= $3
		ORDER BY dist
		LIMIT $4`, mediaID, wsID, maxDist, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SuggestionPair{}
	for rows.Next() {
		var s SuggestionPair
		if err := rows.Scan(&s.A, &s.B, &s.Dist); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// persistSuggestions replaces a movie's rows in the (previously dead)
// face_merge_suggestions table so it reflects the latest computation.
func (p *Plugin) persistSuggestions(ctx context.Context, mediaID string, pairs []SuggestionPair) error {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM face_merge_suggestions WHERE media_id=$1`, mediaID); err != nil {
		return err
	}
	for _, s := range pairs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO face_merge_suggestions (media_id, cluster_a, cluster_b, distance)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (media_id, cluster_a, cluster_b) DO UPDATE SET distance=EXCLUDED.distance`,
			mediaID, s.A, s.B, s.Dist); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SimilarFace is one within-movie nearest-neighbor face.
type SimilarFace struct {
	FaceID       string  `json:"face_id"`
	ClusterID    string  `json:"cluster_id"`
	ClusterLabel string  `json:"cluster_label"`
	PersonID     string  `json:"person_id,omitempty"`
	PersonName   string  `json:"person_name,omitempty"`
	ScreenshotID string  `json:"screenshot_id"`
	TsMs         *int    `json:"ts_ms,omitempty"`
	Box          Box     `json:"box"`
	Score        float64 `json:"score"`
}

// similarFacesInMovie returns the faces in a movie most similar to faceID
// (excluding itself), nearest first. Empty if faceID has no embedding.
func (p *Plugin) similarFacesInMovie(ctx context.Context, wsID, mediaID, faceID string, k int) ([]SimilarFace, error) {
	if k <= 0 || k > 100 {
		k = 12
	}
	rows, err := p.DB.QueryContext(ctx, `
		WITH q AS (
			SELECT embedding FROM faces
			WHERE id=$3 AND media_id=$1 AND workspace_id=$2 AND embedding IS NOT NULL
		)
		SELECT f.id, f.cluster_id, COALESCE(fc.label,''), COALESCE(fc.person_id,''),
		       COALESCE(pe.name,''), f.screenshot_id, f.ts_ms,
		       f.box_x, f.box_y, f.box_w, f.box_h,
		       1 - (f.embedding <=> (SELECT embedding FROM q)) AS score
		FROM faces f
		JOIN face_clusters fc ON fc.id = f.cluster_id
		LEFT JOIN people pe ON pe.id = fc.person_id
		WHERE f.media_id=$1 AND f.workspace_id=$2 AND f.embedding IS NOT NULL AND f.id <> $3
		  AND (SELECT embedding FROM q) IS NOT NULL
		ORDER BY f.embedding <=> (SELECT embedding FROM q)
		LIMIT $4`, mediaID, wsID, faceID, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SimilarFace{}
	for rows.Next() {
		var s SimilarFace
		var ts sql.NullInt64
		if err := rows.Scan(&s.FaceID, &s.ClusterID, &s.ClusterLabel, &s.PersonID, &s.PersonName,
			&s.ScreenshotID, &ts, &s.Box.X, &s.Box.Y, &s.Box.W, &s.Box.H, &s.Score); err != nil {
			return nil, err
		}
		if ts.Valid {
			v := int(ts.Int64)
			s.TsMs = &v
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CrossFilmFace / CrossFilmMovie group cross-film hits by movie.
type CrossFilmFace struct {
	FaceID       string  `json:"face_id"`
	ScreenshotID string  `json:"screenshot_id"`
	TsMs         *int    `json:"ts_ms,omitempty"`
	Box          Box     `json:"box"`
	Score        float64 `json:"score"`
}

type CrossFilmMovie struct {
	MediaID     string          `json:"media_id"`
	Title       string          `json:"title"`
	BestScore   float64         `json:"best_score"`
	AlreadyCast bool            `json:"already_cast"`
	Faces       []CrossFilmFace `json:"faces"`
}

// crossFilmPersonFaces searches every face in the workspace against personID's
// embedding centroid and groups the nearest hits by movie (best score first).
// excludeMedia drops one movie (e.g. the current one). Empty if the person has
// no embedded faces.
func (p *Plugin) crossFilmPersonFaces(ctx context.Context, wsID, personID, excludeMedia string, k int) ([]CrossFilmMovie, error) {
	if k <= 0 || k > 200 {
		k = 24
	}
	rows, err := p.DB.QueryContext(ctx, `
		WITH q AS (
			SELECT avg(f.embedding) AS c
			FROM faces f JOIN face_clusters fc ON fc.id = f.cluster_id
			WHERE fc.person_id=$2 AND f.workspace_id=$1 AND f.embedding IS NOT NULL
		)
		SELECT f.media_id, m.title, f.id, f.screenshot_id, f.ts_ms,
		       f.box_x, f.box_y, f.box_w, f.box_h,
		       1 - (f.embedding <=> (SELECT c FROM q)) AS score,
		       EXISTS(SELECT 1 FROM media_people mp WHERE mp.media_id=f.media_id AND mp.person_id=$2) AS already_cast
		FROM faces f JOIN media_items m ON m.id = f.media_id
		WHERE f.workspace_id=$1 AND f.embedding IS NOT NULL
		  AND (SELECT c FROM q) IS NOT NULL
		  AND ($3='' OR f.media_id <> $3)
		ORDER BY f.embedding <=> (SELECT c FROM q)
		LIMIT $4`, wsID, personID, excludeMedia, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CrossFilmMovie{}
	idx := map[string]int{}
	for rows.Next() {
		var mediaID, title string
		var cf CrossFilmFace
		var ts sql.NullInt64
		var cast bool
		if err := rows.Scan(&mediaID, &title, &cf.FaceID, &cf.ScreenshotID, &ts,
			&cf.Box.X, &cf.Box.Y, &cf.Box.W, &cf.Box.H, &cf.Score, &cast); err != nil {
			return nil, err
		}
		if ts.Valid {
			v := int(ts.Int64)
			cf.TsMs = &v
		}
		i, ok := idx[mediaID]
		if !ok {
			// rows are score-desc, so the first hit per movie is its best.
			out = append(out, CrossFilmMovie{MediaID: mediaID, Title: title, BestScore: cf.Score, AlreadyCast: cast})
			i = len(out) - 1
			idx[mediaID] = i
		}
		out[i].Faces = append(out[i].Faces, cf)
	}
	return out, rows.Err()
}
