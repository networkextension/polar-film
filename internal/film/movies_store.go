package film

// movies_store.go — persistence for media_items (M1: movies). Everything is
// workspace-scoped. Binaries (poster) are referenced by asset_id only.

import (
	"context"
	"database/sql"
	"time"
)

// Movie is a media_items row.
type Movie struct {
	ID            string    `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Kind          string    `json:"kind"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"original_title,omitempty"`
	Year          *int      `json:"year,omitempty"`
	Country       string    `json:"country,omitempty"`
	Language      string    `json:"language,omitempty"`
	RuntimeMin    *int      `json:"runtime_min,omitempty"`
	Summary       string    `json:"summary,omitempty"`
	PosterAssetID string    `json:"poster_asset_id,omitempty"`
	ImdbID        string    `json:"imdb_id,omitempty"`
	DoubanID      string    `json:"douban_id,omitempty"`
	TmdbID        string    `json:"tmdb_id,omitempty"`
	// ParentID links an episode to its series / a podcast episode to its
	// show (M7 generalization). nil for standalone movies / top-level items.
	ParentID  *string   `json:"parent_id,omitempty"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`

	// --- TMDB enrichment (M9). All nil/empty until /enrich runs. ---
	ReleaseDate *string    `json:"release_date,omitempty"` // "2007-05-23" (DATE rendered as string)
	Rating      *float64   `json:"rating,omitempty"`       // TMDB vote_average 0–10
	BackdropURL string     `json:"backdrop_url,omitempty"`
	PosterURL   string     `json:"poster_url,omitempty"` // TMDB CDN poster (fallback when no poster_asset_id)
	Tagline     string     `json:"tagline,omitempty"`
	Overview    string     `json:"overview,omitempty"` // TMDB plot; distinct from user Summary
	EnrichedAt  *time.Time `json:"enriched_at,omitempty"`

	// --- filmscan processing status (M10). Surfaced as a 处理中 chip on the
	// movie page so an operator can see which stage a scan is at. Driven by
	// the filmscan orchestration POSTing /scan-status, and auto-set to "done"
	// when subtitles land. ---
	ScanStatus    string     `json:"scan_status,omitempty"`     // ""|extracting|extracted|analyzing|done|failed
	ScanDetail    string     `json:"scan_detail,omitempty"`     // free text, e.g. "转写中" / "2491 帧"
	ScanUpdatedAt *time.Time `json:"scan_updated_at,omitempty"`
}

func nullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStr(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

func scanIntPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

func scanStrPtr(s sql.NullString) *string {
	if !s.Valid || s.String == "" {
		return nil
	}
	v := s.String
	return &v
}

func scanFloatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func scanTimePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

const movieCols = `id, workspace_id, kind, title, original_title, year, country, language,
	runtime_min, summary, poster_asset_id, imdb_id, douban_id, tmdb_id, parent_id, created_by, created_at,
	release_date, rating, backdrop_url, poster_url, tagline, overview, enriched_at,
	scan_status, scan_detail, scan_updated_at`

func scanMovie(row interface{ Scan(...any) error }) (Movie, error) {
	var m Movie
	var year, runtime sql.NullInt64
	var parentID, backdrop, posterURL, tagline, overview, scanStatus, scanDetail sql.NullString
	var releaseDate, enrichedAt, scanUpdatedAt sql.NullTime
	var rating sql.NullFloat64
	err := row.Scan(&m.ID, &m.WorkspaceID, &m.Kind, &m.Title, &m.OriginalTitle, &year,
		&m.Country, &m.Language, &runtime, &m.Summary, &m.PosterAssetID,
		&m.ImdbID, &m.DoubanID, &m.TmdbID, &parentID, &m.CreatedBy, &m.CreatedAt,
		&releaseDate, &rating, &backdrop, &posterURL, &tagline, &overview, &enrichedAt,
		&scanStatus, &scanDetail, &scanUpdatedAt)
	if err != nil {
		return Movie{}, err
	}
	m.Year = scanIntPtr(year)
	m.RuntimeMin = scanIntPtr(runtime)
	m.ParentID = scanStrPtr(parentID)
	if releaseDate.Valid {
		d := releaseDate.Time.Format("2006-01-02")
		m.ReleaseDate = &d
	}
	m.Rating = scanFloatPtr(rating)
	m.BackdropURL = backdrop.String
	m.PosterURL = posterURL.String
	m.Tagline = tagline.String
	m.Overview = overview.String
	m.EnrichedAt = scanTimePtr(enrichedAt)
	m.ScanStatus = scanStatus.String
	m.ScanDetail = scanDetail.String
	m.ScanUpdatedAt = scanTimePtr(scanUpdatedAt)
	return m, nil
}

// setScanStatus upserts the filmscan processing status for a movie. status ""
// clears it; detail is free text. Stamps scan_updated_at. Used by the
// /scan-status endpoint + the auto-set-on-subtitle path.
func (p *Plugin) setScanStatus(ctx context.Context, wsID, id, status, detail string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE media_items SET scan_status=NULLIF($3,''), scan_detail=NULLIF($4,''), scan_updated_at=now()
		WHERE workspace_id=$1 AND id=$2`, wsID, id, status, detail)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// updateMovieEnrichment writes only the M9 TMDB columns + stamps enriched_at.
// Separate from updateMovie so /enrich never clobbers user-editable fields.
// releaseDate is "YYYY-MM-DD" or "" (→ NULL); rating nil → NULL.
func (p *Plugin) updateMovieEnrichment(ctx context.Context, wsID, id, releaseDate string, rating *float64,
	backdropURL, posterURL, tagline, overview string) (bool, error) {
	var rd any
	if releaseDate != "" {
		rd = releaseDate
	}
	res, err := p.DB.ExecContext(ctx, `
		UPDATE media_items SET
			release_date=$3, rating=$4, backdrop_url=NULLIF($5,''), poster_url=NULLIF($6,''),
			tagline=NULLIF($7,''), overview=NULLIF($8,''), enriched_at=now()
		WHERE workspace_id=$1 AND id=$2`,
		wsID, id, rd, rating, backdropURL, posterURL, tagline, overview)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// listMoviesNeedEnrich returns movies in the workspace that haven't been
// enriched yet (enriched_at IS NULL), newest first, capped at limit. Used by
// the batch backfill.
func (p *Plugin) listMoviesNeedEnrich(ctx context.Context, wsID string, limit int) ([]Movie, error) {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT `+movieCols+` FROM media_items WHERE workspace_id=$1 AND enriched_at IS NULL
		 ORDER BY created_at DESC LIMIT $2`, wsID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Plugin) insertMovie(ctx context.Context, m Movie) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO media_items
			(id, workspace_id, kind, title, original_title, year, country, language,
			 runtime_min, summary, poster_asset_id, imdb_id, douban_id, tmdb_id, parent_id, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16, now())`,
		m.ID, m.WorkspaceID, m.Kind, m.Title, m.OriginalTitle, nullInt(m.Year), m.Country, m.Language,
		nullInt(m.RuntimeMin), m.Summary, m.PosterAssetID, m.ImdbID, m.DoubanID, m.TmdbID, nullStr(m.ParentID), m.CreatedBy)
	return err
}

func (p *Plugin) listMovies(ctx context.Context, wsID, kind string) ([]Movie, error) {
	q := `SELECT ` + movieCols + ` FROM media_items WHERE workspace_id=$1`
	args := []any{wsID}
	if kind != "" {
		q += ` AND kind=$2`
		args = append(args, kind)
	}
	q += ` ORDER BY created_at DESC`
	rows, err := p.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// listChildren returns media items whose parent_id == parentID (episodes of
// a series, podcast episodes), ordered by year then title.
func (p *Plugin) listChildren(ctx context.Context, wsID, parentID string) ([]Movie, error) {
	rows, err := p.DB.QueryContext(ctx,
		`SELECT `+movieCols+` FROM media_items WHERE workspace_id=$1 AND parent_id=$2 ORDER BY year NULLS LAST, title`,
		wsID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *Plugin) getMovie(ctx context.Context, wsID, id string) (Movie, error) {
	row := p.DB.QueryRowContext(ctx, `SELECT `+movieCols+` FROM media_items WHERE workspace_id=$1 AND id=$2`, wsID, id)
	return scanMovie(row)
}

func (p *Plugin) updateMovie(ctx context.Context, m Movie) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE media_items SET
			kind=$3, title=$4, original_title=$5, year=$6, country=$7, language=$8,
			runtime_min=$9, summary=$10, poster_asset_id=$11, imdb_id=$12, douban_id=$13, tmdb_id=$14, parent_id=$15
		WHERE workspace_id=$1 AND id=$2`,
		m.WorkspaceID, m.ID, m.Kind, m.Title, m.OriginalTitle, nullInt(m.Year), m.Country, m.Language,
		nullInt(m.RuntimeMin), m.Summary, m.PosterAssetID, m.ImdbID, m.DoubanID, m.TmdbID, nullStr(m.ParentID))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *Plugin) deleteMovie(ctx context.Context, wsID, id string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM media_items WHERE workspace_id=$1 AND id=$2`, wsID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
