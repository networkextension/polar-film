package film

// movies_scan_status.go — M10 filmscan processing-status reporting.
//   POST /api/film/movies/:id/scan-status {status, detail?}
// The filmscan orchestration POSTs a stage transition at each step so the
// movie page can show a 处理中 chip. Status is also auto-set to "done" when
// subtitles land (see subtitles_handlers.go).

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// scanStatuses is the accepted set (a free-form detail rides alongside).
var scanStatuses = map[string]bool{
	"": true, "pending": true, "extracting": true, "extracted": true,
	"analyzing": true, "done": true, "failed": true,
}

type scanStatusReq struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func (p *Plugin) handleScanStatus(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	id := strings.TrimSpace(c.Param("id"))

	var req scanStatusReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	req.Status = strings.TrimSpace(req.Status)
	if !scanStatuses[req.Status] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown status: " + req.Status})
		return
	}

	ok, err := p.setScanStatus(ctx, wsID, id, req.Status, strings.TrimSpace(req.Detail))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	m, err := p.getMovie(ctx, wsID, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"scan_status": m.ScanStatus, "scan_detail": m.ScanDetail, "scan_updated_at": m.ScanUpdatedAt})
}
