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
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const enrichCastLimit = 12 // top-billed cast attached per movie

// trailingYear matches a " (1954)" suffix on a title.
var trailingYear = regexp.MustCompile(`\s*\((\d{4})\)\s*$`)

// cleanSearchTitle strips a trailing "(YYYY)" from a title for the TMDB query
// and returns the extracted year (0 if none). "Dial M for Murder (1954)" →
// ("Dial M for Murder", 1954).
func cleanSearchTitle(title string) (string, int) {
	yr := 0
	if m := trailingYear.FindStringSubmatch(title); m != nil {
		yr, _ = strconv.Atoi(m[1])
		title = trailingYear.ReplaceAllString(title, "")
	}
	return strings.TrimSpace(title), yr
}

// ensurePerson lives in faces_store.go (transactional, shared with the faces
// path). The enrich path reuses it — identical signature.

// enrichOneMovie does the resolve→fetch→writeback→cast for a single movie and
// returns how many cast members were attached. Errors are returned (the caller
// decides HTTP status / batch accounting).
func (p *Plugin) enrichOneMovie(ctx context.Context, wsID string, m Movie) (int, error) {
	// 1. Resolve a tmdb_id if the movie doesn't have one yet (search title+year).
	if strings.TrimSpace(m.TmdbID) == "" {
		raw := strings.TrimSpace(m.Title)
		if raw == "" {
			raw = strings.TrimSpace(m.OriginalTitle)
		}
		if raw == "" {
			return 0, errors.New("movie has no title to resolve against TMDB")
		}
		// Titles often carry the year inline ("Dial M for Murder (1954)"),
		// which breaks the TMDB query — strip it and use it as the year hint.
		title, titleYr := cleanSearchTitle(raw)
		yr := 0
		if m.Year != nil {
			yr = *m.Year
		} else if titleYr > 0 {
			yr = titleYr
		}
		id, err := p.tmdb.searchMovie(ctx, title, yr)
		if err != nil {
			return 0, fmt.Errorf("tmdb search: %w", err)
		}
		if id == 0 && yr > 0 { // retry without the year filter (release-year drift)
			id, err = p.tmdb.searchMovie(ctx, title, 0)
			if err != nil {
				return 0, fmt.Errorf("tmdb search: %w", err)
			}
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
	out := p.fillMovie(ctx, fresh) // {movie, cast, tags}
	out["cast_count"] = castN
	c.JSON(http.StatusOK, out)
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
