package film

// subtitles_handlers.go — subtitle upload/list/segments + 台词 search.
//
//   POST   /api/film/movies/:id/subtitles   {lang, format, content}
//   GET    /api/film/movies/:id/subtitles
//   GET    /api/film/subtitles/:subId/segments
//   DELETE /api/film/subtitles/:subId
//   GET    /api/film/search?q=&media_id=&limit=

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) handleSubtitleUpload(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))

	var req struct {
		Lang    string `json:"lang"`
		Format  string `json:"format"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Lang = strings.TrimSpace(req.Lang)
	if req.Lang == "" || strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "lang and content are required"})
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format == "" {
		format = "srt"
	}
	if _, err := p.getMovie(ctx, wsID, mediaID); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	cues := parseCues(req.Content)
	if len(cues) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no subtitle cues parsed (expected SRT/VTT)"})
		return
	}
	s := Subtitle{ID: newID("sub_"), WorkspaceID: wsID, MediaID: mediaID, Lang: req.Lang, Format: format, Source: "uploaded"}
	if err := p.insertSubtitleWithSegments(ctx, s, cues); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// Best-effort semantic indexing: segments stay keyword-searchable even
	// if the embedder is down; reindex backfills NULLs later.
	embedded, eerr := p.embedSegments(ctx, wsID, s.ID)
	if eerr != nil {
		log.Printf("film: embed segments for %s failed (keyword search still works): %v", s.ID, eerr)
	}
	// M10: subtitles landing = the filmscan analyze tier finished → mark done so
	// the 处理中 chip clears even if the orchestration didn't POST a final status.
	if _, serr := p.setScanStatus(ctx, wsID, mediaID, "done", "字幕已就绪"); serr != nil {
		log.Printf("film: set scan_status done for %s failed: %v", mediaID, serr)
	}
	c.JSON(http.StatusCreated, gin.H{"subtitle": s, "segments": len(cues), "embedded": embedded})
}

func (p *Plugin) handleSubtitleList(c *gin.Context) {
	subs, err := p.listSubtitles(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"subtitles": subs})
}

func (p *Plugin) handleSubtitleSegments(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	subID := strings.TrimSpace(c.Param("subId"))
	if !p.subtitleExists(ctx, wsID, subID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "subtitle not found"})
		return
	}
	segs, err := p.listSegments(ctx, wsID, subID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"segments": segs})
}

func (p *Plugin) handleSubtitleDelete(c *gin.Context) {
	ok, err := p.deleteSubtitle(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("subId")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "subtitle not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// handleSearch searches subtitle segments. mode=keyword (default) is the
// M2 台词 substring match; mode=semantic embeds the query and runs a
// pgvector cosine kNN (M4). Both return SearchHit; semantic adds a score.
func (p *Plugin) handleSearch(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "q is required"})
		return
	}
	limit := 0
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	mediaID := strings.TrimSpace(c.Query("media_id"))
	mode := strings.ToLower(strings.TrimSpace(c.Query("mode")))
	if mode == "" {
		mode = "keyword"
	}
	p.metrics.incSearch(mode)

	if mode == "semantic" {
		vecs, err := p.embedder.Embed(ctx, []string{q})
		if err != nil || len(vecs) != 1 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "embed query failed: " + errString(err)})
			return
		}
		hits, err := p.semanticSearchSegments(ctx, wsID, vecs[0], mediaID, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"kind": "subtitle", "mode": "semantic", "query": q, "hits": hits})
		return
	}

	hits, err := p.searchSegments(ctx, wsID, q, mediaID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kind": "subtitle", "mode": "keyword", "query": q, "hits": hits})
}

func errString(err error) string {
	if err == nil {
		return "empty embedding result"
	}
	return err.Error()
}

// handleReindex backfills embeddings for segments and movies in the caller's
// workspace that don't have one yet (e.g. data created before M4, or while
// the embedder was down). Idempotent.
func (p *Plugin) handleReindex(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	limit := 0
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	segs, err := p.embedPendingSegments(ctx, wsID, limit)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "embed segments: " + err.Error(), "segments_embedded": segs})
		return
	}
	movies, err := p.embedPendingMovies(ctx, wsID, limit)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "embed movies: " + err.Error(), "segments_embedded": segs, "movies_embedded": movies})
		return
	}
	// Backfill person_id for named-speaker segments ingested before P4b.
	speakers, err := p.resolveSpeakersForWorkspace(ctx, wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resolve speakers: " + err.Error(), "segments_embedded": segs, "movies_embedded": movies, "speakers_resolved": speakers})
		return
	}
	c.JSON(http.StatusOK, gin.H{"segments_embedded": segs, "movies_embedded": movies, "speakers_resolved": speakers, "embedder": p.embedder.Name()})
}

// handleSimilarMovies returns movies nearest to :id by movie-level
// embedding cosine ("相似片").
func (p *Plugin) handleSimilarMovies(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	id := strings.TrimSpace(c.Param("id"))
	if _, err := p.getMovie(ctx, wsID, id); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	limit := 0
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	out, err := p.similarMovies(ctx, wsID, id, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"media_id": id, "similar": out})
}
