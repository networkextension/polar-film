package film

// movies_enrichment.go — M9 TMDB metadata enrichment.
//   POST /api/film/movies/:id/enrich        — enrich one movie
//   POST /api/film/movies/batch/enrich      — backfill un-enriched movies
// Resolve tmdb_id (search by title+year) if absent, fetch details + credits,
// write back metadata, attach the top-billed cast as people.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const enrichCastLimit = 12 // top-billed cast attached per movie

// ensurePerson returns the workspace's person id for this name, creating it if
// absent (idempotent). Non-tx sibling of ensurePersonTx, for the enrich path.
func (p *Plugin) ensurePerson(ctx context.Context, wsID, name string) (string, error) {
	var id string
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO people (id, workspace_id, name)
		VALUES ($1,$2,$3)
		ON CONFLICT (workspace_id, name) DO UPDATE SET name=EXCLUDED.name
		RETURNING id`,
		newID("pe_"), wsID, name).Scan(&id)
	return id, err
}

// enrichOneMovie does the resolve→fetch→writeback→cast for a single movie and
// returns how many cast members were attached. Errors are returned (the caller
// decides HTTP status / batch accounting).
func (p *Plugin) enrichOneMovie(ctx context.Context, wsID string, m Movie) (int, error) {
	// 1. Resolve a tmdb_id if the movie doesn't have one yet (search title+year).
	if strings.TrimSpace(m.TmdbID) == "" {
		title := strings.TrimSpace(m.Title)
		if title == "" {
			title = strings.TrimSpace(m.OriginalTitle)
		}
		if title == "" {
			return 0, errors.New("movie has no title to resolve against TMDB")
		}
		yr := 0
		if m.Year != nil {
			yr = *m.Year
		}
		id, err := p.tmdb.searchMovie(ctx, title, yr)
		if err != nil {
			return 0, fmt.Errorf("tmdb search: %w", err)
		}
		if id == 0 {
			return 0, fmt.Errorf("TMDB 未找到匹配:%q (%d)", title, yr)
		}
		m.TmdbID = strconv.Itoa(id)
		if _, err := p.updateMovie(ctx, m); err != nil { // persist the resolved id
			return 0, fmt.Errorf("persist tmdb_id: %w", err)
		}
	}

	tid, err := strconv.Atoi(strings.TrimSpace(m.TmdbID))
	if err != nil {
		return 0, fmt.Errorf("无效 tmdb_id %q", m.TmdbID)
	}

	// 2. Fetch details + credits.
	tm, err := p.tmdb.fetchMovie(ctx, tid)
	if err != nil {
		return 0, fmt.Errorf("tmdb fetch: %w", err)
	}

	// 3. Writeback metadata.
	var rating *float64
	if tm.VoteAverage > 0 {
		v := tm.VoteAverage
		rating = &v
	}
	backdrop := tmdbImageURL(tmdbBackdropSize, tm.BackdropPath)
	poster := tmdbImageURL(tmdbPosterSize, tm.PosterPath)
	if _, err := p.updateMovieEnrichment(ctx, wsID, m.ID, tm.ReleaseDate, rating, backdrop, poster, tm.Tagline, tm.Overview); err != nil {
		return 0, fmt.Errorf("writeback: %w", err)
	}

	// 4. Attach top-billed cast as workspace people.
	n := 0
	for i, cm := range tm.Credits.Cast {
		if i >= enrichCastLimit {
			break
		}
		name := strings.TrimSpace(cm.Name)
		if name == "" {
			continue
		}
		pid, err := p.ensurePerson(ctx, wsID, name)
		if err != nil {
			continue
		}
		if err := p.attachPerson(ctx, wsID, m.ID, pid, "actor", strings.TrimSpace(cm.Character), i); err != nil {
			continue
		}
		n++
	}
	return n, nil
}

func (p *Plugin) handleMovieEnrich(c *gin.Context) {
	if !p.tmdb.enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "TMDB 未配置（在 film-svc 设置 POLAR_FILM_TMDB_TOKEN）"})
		return
	}
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	id := strings.TrimSpace(c.Param("id"))
	m, err := p.getMovie(ctx, wsID, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	castN, err := p.enrichOneMovie(ctx, wsID, m)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	fresh, _ := p.getMovie(ctx, wsID, id)
	c.JSON(http.StatusOK, gin.H{"movie": p.fillMovie(ctx, fresh), "cast_count": castN})
}

func (p *Plugin) handleMovieEnrichBatch(c *gin.Context) {
	if !p.tmdb.enabled() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "TMDB 未配置（在 film-svc 设置 POLAR_FILM_TMDB_TOKEN）"})
		return
	}
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	const limit = 25 // bounded per request; call again to continue
	movies, err := p.listMoviesNeedEnrich(ctx, wsID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	results := make([]gin.H, 0, len(movies))
	ok := 0
	for _, m := range movies {
		castN, err := p.enrichOneMovie(ctx, wsID, m)
		if err != nil {
			results = append(results, gin.H{"id": m.ID, "title": m.Title, "error": err.Error()})
			continue
		}
		ok++
		results = append(results, gin.H{"id": m.ID, "title": m.Title, "cast": castN})
	}
	c.JSON(http.StatusOK, gin.H{"processed": len(movies), "ok": ok, "results": results})
}
