package film

// process_handlers.go — kick off fleet video processing for a movie.
//
//   POST /api/film/movies/:id/process  {video_url}
//
// The film knowledge base does NOT store source video. The caller supplies a
// video_url the fleet agent can GET. We submit a `film.extract` compute-task
// (task-processing v2 — the unified compute-tasks queue); an online agent runs
// the extract stage (audio→music lib, keyframes/faces→photo lib), then dock
// fires our signed /internal/v1/film/scan-callback, which chains the ANE
// `film.analyze` stage and pushes the SRT back to THIS movie. scan_status tracks
// each stage. See doc/arch/task-processing-v2.md.

import (
	"encoding/json"
	"net/http"
	"strings"

	sdk "github.com/networkextension/polar-sdk"

	"github.com/gin-gonic/gin"
)

type processMovieReq struct {
	VideoURL string `json:"video_url"`
}

func (p *Plugin) handleMovieProcess(c *gin.Context) {
	ctx := c.Request.Context()
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

	// Stage 1: extract (any-arch, incl. cheap x86). The agent shells `filmscan
	// extract`, which uploads audio→music lib + keyframes→photo lib as the
	// requesting user — so we forward the caller's token + workspace. The agent's
	// FILMSCAN_SERVER env supplies the public base that routes /api/tracks +
	// /api/photo + /api/film. (TTL note: user token ~30min; extract finishes in
	// minutes. Durable fix = a dock-minted task token — task-processing-v2 §follow-ups.)
	input, _ := json.Marshal(map[string]any{
		"video_url":     strings.TrimSpace(req.VideoURL),
		"media_id":      mediaID,
		"workspace_id":  wsID,
		"forward_token": extractAccessToken(c),
	})
	task, err := p.Dock.SubmitComputeTask(sdk.SubmitComputeTaskRequest{
		WorkspaceID:  wsID,
		Skill:        "film.extract",
		Input:        input,
		CallbackPath: "/internal/v1/film/scan-callback",
		RequesterRef: mediaID,
		AutoStart:    true, // straight to queued (no manual release)
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "submit film.extract: " + err.Error()})
		return
	}
	_, _ = p.setScanStatus(ctx, wsID, mediaID, "extracting", "排队中")
	c.JSON(http.StatusAccepted, gin.H{"task_id": task.ID, "status": "extracting"})
}
