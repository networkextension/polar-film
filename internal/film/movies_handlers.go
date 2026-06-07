package film

// movies_handlers.go — /api/film/movies CRUD.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type createMovieReq struct {
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          *int   `json:"year"`
	Country       string `json:"country"`
	Language      string `json:"language"`
	RuntimeMin    *int   `json:"runtime_min"`
	Summary       string `json:"summary"`
	PosterAssetID string `json:"poster_asset_id"`
	ImdbID        string `json:"imdb_id"`
	DoubanID      string `json:"douban_id"`
	TmdbID        string `json:"tmdb_id"`
}

type updateMovieReq struct {
	Kind          *string `json:"kind"`
	Title         *string `json:"title"`
	OriginalTitle *string `json:"original_title"`
	Year          *int    `json:"year"`
	Country       *string `json:"country"`
	Language      *string `json:"language"`
	RuntimeMin    *int    `json:"runtime_min"`
	Summary       *string `json:"summary"`
	PosterAssetID *string `json:"poster_asset_id"`
	ImdbID        *string `json:"imdb_id"`
	DoubanID      *string `json:"douban_id"`
	TmdbID        *string `json:"tmdb_id"`
}

// applyMoviePatch overlays provided (non-nil) fields onto cur. Pure — tested.
func applyMoviePatch(cur Movie, req updateMovieReq) Movie {
	if req.Kind != nil {
		cur.Kind = strings.TrimSpace(*req.Kind)
	}
	if req.Title != nil {
		cur.Title = *req.Title
	}
	if req.OriginalTitle != nil {
		cur.OriginalTitle = *req.OriginalTitle
	}
	if req.Year != nil {
		cur.Year = req.Year
	}
	if req.Country != nil {
		cur.Country = *req.Country
	}
	if req.Language != nil {
		cur.Language = *req.Language
	}
	if req.RuntimeMin != nil {
		cur.RuntimeMin = req.RuntimeMin
	}
	if req.Summary != nil {
		cur.Summary = *req.Summary
	}
	if req.PosterAssetID != nil {
		cur.PosterAssetID = *req.PosterAssetID
	}
	if req.ImdbID != nil {
		cur.ImdbID = *req.ImdbID
	}
	if req.DoubanID != nil {
		cur.DoubanID = *req.DoubanID
	}
	if req.TmdbID != nil {
		cur.TmdbID = *req.TmdbID
	}
	return cur
}

func (p *Plugin) handleMovieCreate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)

	var req createMovieReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	if req.Title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title is required"})
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "movie"
	}
	m := Movie{
		ID: newID("mv_"), WorkspaceID: wsID, Kind: kind, Title: req.Title,
		OriginalTitle: req.OriginalTitle, Year: req.Year, Country: req.Country, Language: req.Language,
		RuntimeMin: req.RuntimeMin, Summary: req.Summary, PosterAssetID: req.PosterAssetID,
		ImdbID: req.ImdbID, DoubanID: req.DoubanID, TmdbID: req.TmdbID, CreatedBy: userID,
	}
	if err := p.insertMovie(ctx, m); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a " + kind + " with this title/year already exists in this workspace"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, p.fillMovie(ctx, m))
}

func (p *Plugin) handleMovieList(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	movies, err := p.listMovies(c.Request.Context(), wsID, strings.TrimSpace(c.Query("kind")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"movies": movies})
}

func (p *Plugin) handleMovieDetail(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	m, err := p.getMovie(ctx, wsID, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p.fillMovie(ctx, m))
}

func (p *Plugin) handleMovieUpdate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	id := strings.TrimSpace(c.Param("id"))

	var req updateMovieReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	cur, err := p.getMovie(ctx, wsID, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	upd := applyMoviePatch(cur, req)
	if strings.TrimSpace(upd.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "title cannot be empty"})
		return
	}
	if _, err := p.updateMovie(ctx, upd); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "title/year collides with another item"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, p.fillMovie(ctx, upd))
}

func (p *Plugin) handleMovieDelete(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	ok, err := p.deleteMovie(c.Request.Context(), wsID, strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// fillMovie augments a movie with its cast + tags for detail/create responses.
func (p *Plugin) fillMovie(ctx context.Context, m Movie) gin.H {
	cast, _ := p.listMoviePeople(ctx, m.WorkspaceID, m.ID)
	tags, _ := p.listMovieTags(ctx, m.WorkspaceID, m.ID)
	return gin.H{"movie": m, "cast": cast, "tags": tags}
}
