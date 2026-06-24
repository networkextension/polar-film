package film

// process_handlers.go — kick off fleet video processing for a movie.
//
//   POST /api/film/movies/:id/process  {video_url}
//
// The film knowledge base does NOT store source video (analyze_jobs operates on
// text already ingested). So the caller supplies a video_url the fleet agent can
// GET. We enqueue a durable filmscan_extract job on dock; an online agent (x86 or
// arm64) runs the extract stage (audio→music lib, keyframes/faces→photo lib), and
// dock auto-chains the ANE analyze stage, which pushes the SRT back to THIS movie.
// See internal/app/dock/agent_jobs_handlers.go.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type processMovieReq struct {
	VideoURL string `json:"video_url"`
}

func (p *Plugin) handleMovieProcess(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))
	if wsID == "" || mediaID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspace and movie id required"})
		return
	}
	var req processMovieReq
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.VideoURL) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "video_url required"})
		return
	}

	body := map[string]any{
		"workspace_id": wsID,
		"kind":         "filmscan_extract",
		// required_arch omitted → NULL → any agent (extract never stalls).
		"payload": map[string]any{
			"video_url":    strings.TrimSpace(req.VideoURL),
			"workspace_id": wsID,
			"media_id":     mediaID,
		},
	}
	resp, err := p.Dock.Do(http.MethodPost, "/internal/v1/agent-tasks/enqueue", body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "enqueue: " + err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		c.JSON(http.StatusBadGateway, gin.H{"error": "enqueue HTTP " + strconv.Itoa(resp.StatusCode) + ": " + e.Error})
		return
	}
	var out struct {
		JobID int64 `json:"job_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	c.JSON(http.StatusAccepted, gin.H{"job_id": out.JobID, "status": "queued"})
}
