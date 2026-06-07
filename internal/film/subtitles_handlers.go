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
	c.JSON(http.StatusCreated, gin.H{"subtitle": s, "segments": len(cues)})
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

// handleSearch — M2 keyword (台词) search over subtitle segments. Vector /
// screenshot search arrive in M4+.
func (p *Plugin) handleSearch(c *gin.Context) {
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
	hits, err := p.searchSegments(c.Request.Context(), wsID, q, strings.TrimSpace(c.Query("media_id")), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"kind": "subtitle", "query": q, "hits": hits})
}
