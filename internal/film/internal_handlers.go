package film

// internal_handlers.go — dock→plugin loopback surface (M7 hardening).
//
//   POST /internal/v1/film/workspace-deleted  {workspace_id}
//
// Purges all of a workspace's film data. Gated loopback-only (same pattern
// as polar-hosts internal endpoints): in the standard deploy dock + film-svc
// share a host and nginx blocks /internal/ externally.
//
// NOTE: dock does not yet fan out a workspace-deletion webhook (it refuses
// team deletion until its own owned tables are empty and can't see external
// plugin DBs). This endpoint is therefore ready for that future fan-out and
// usable now as an ops purge (`curl 127.0.0.1:8102/internal/v1/film/...`).

import (
	"context"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"
)

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *Plugin) handleInternalWorkspaceDeleted(c *gin.Context) {
	if !isLoopbackRequest(c.Request) {
		c.JSON(http.StatusForbidden, gin.H{"error": "loopback only"})
		return
	}
	var req struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.WorkspaceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "workspace_id required"})
		return
	}
	counts, err := p.purgeWorkspace(c.Request.Context(), req.WorkspaceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"workspace_id": req.WorkspaceID, "deleted": counts})
}

// purgeWorkspace removes every film row owned by a workspace in one tx.
// Deleting media_items cascades to subtitles/segments/screenshots/timeline/
// tags-links/people-links/media_embeddings/analyze_jobs via ON DELETE
// CASCADE; people + tags are workspace-scoped (not media-FK'd) so they're
// deleted explicitly.
func (p *Plugin) purgeWorkspace(ctx context.Context, wsID string) (map[string]int64, error) {
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	counts := map[string]int64{}
	for _, t := range []string{"media_items", "people", "tags"} {
		res, err := tx.ExecContext(ctx, "DELETE FROM "+t+" WHERE workspace_id=$1", wsID)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		counts[t] = n
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return counts, nil
}
