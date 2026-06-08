package film

// analyze_handlers.go — M5 AI pipeline API.
//
//   POST /api/film/movies/:id/analyze   {llm_config_id, steps?:[...]} → job (async)
//   GET  /api/film/movies/:id/analyze   → latest job for the movie
//   GET  /api/film/analyze/:jobId       → one job's status/steps
//   GET  /api/film/movies/:id/timeline  → the (AI-generated) plot timeline

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type analyzeReq struct {
	LLMConfigID int64    `json:"llm_config_id"`
	Steps       []string `json:"steps"` // optional subset; empty → all
}

func (p *Plugin) handleAnalyzeStart(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))

	if _, err := p.getMovie(ctx, wsID, mediaID); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var req analyzeReq
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if req.LLMConfigID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "llm_config_id is required (pick a workspace LLM config)"})
		return
	}

	// Build the requested step set (default = all).
	steps := map[string]AnalyzeStep{}
	want := req.Steps
	if len(want) == 0 {
		want = analyzeStepNames
	}
	for _, s := range want {
		s = strings.ToLower(strings.TrimSpace(s))
		if isAnalyzeStep(s) {
			steps[s] = AnalyzeStep{Status: "pending"}
		}
	}
	if len(steps) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no valid steps (allowed: summary, tags, timeline)"})
		return
	}

	if existing, busy := p.hasActiveJob(ctx, wsID, mediaID); busy {
		c.JSON(http.StatusConflict, gin.H{"error": "an analyze job is already running for this movie", "job_id": existing})
		return
	}

	job, err := p.createAnalyzeJob(ctx, wsID, mediaID, steps)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	go p.runAnalyzeJob(job, req.LLMConfigID)
	c.JSON(http.StatusAccepted, job)
}

func (p *Plugin) handleAnalyzeJobGet(c *gin.Context) {
	job, err := p.getAnalyzeJob(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("jobId")))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "job not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, job)
}

func (p *Plugin) handleAnalyzeLatest(c *gin.Context) {
	job, err := p.latestJobForMedia(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("id")))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no analyze job for this movie yet"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, job)
}

func (p *Plugin) handleTimelineList(c *gin.Context) {
	beats, err := p.listTimeline(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"timeline": beats})
}

func isAnalyzeStep(s string) bool {
	for _, n := range analyzeStepNames {
		if n == s {
			return true
		}
	}
	return false
}
