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

const movieCols = `id, workspace_id, kind, title, original_title, year, country, language,
	runtime_min, summary, poster_asset_id, imdb_id, douban_id, tmdb_id, parent_id, created_by, created_at`

func scanMovie(row interface{ Scan(...any) error }) (Movie, error) {
	var m Movie
	var year, runtime sql.NullInt64
	var parentID sql.NullString
	err := row.Scan(&m.ID, &m.WorkspaceID, &m.Kind, &m.Title, &m.OriginalTitle, &year,
		&m.Country, &m.Language, &runtime, &m.Summary, &m.PosterAssetID,
		&m.ImdbID, &m.DoubanID, &m.TmdbID, &parentID, &m.CreatedBy, &m.CreatedAt)
	if err != nil {
		return Movie{}, err
	}
	m.Year = scanIntPtr(year)
	m.RuntimeMin = scanIntPtr(runtime)
	m.ParentID = scanStrPtr(parentID)
	return m, nil
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
