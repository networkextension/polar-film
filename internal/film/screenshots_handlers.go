package film

// screenshots_handlers.go — upload screenshots (multipart) to polar-assets
// via the SDK, store pointers + phash, list with signed URLs, delete.
//
//   POST   /api/film/movies/:id/screenshots   (multipart: file[], ts_ms[])
//   GET    /api/film/movies/:id/screenshots
//   GET    /api/film/screenshots/:scId/url     → dock-signed blob URL
//   DELETE /api/film/screenshots/:scId
//
// Image bytes never live in polar_film — only an asset_id (the dock asset
// catalog id) plus a perceptual hash. Requires the dock asset subsystem
// (/internal/v1/assets/*) + a registered assets provider at runtime.

import (
	"bytes"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	sdk "github.com/networkextension/polar-sdk"
)

func (p *Plugin) handleScreenshotUpload(c *gin.Context) {
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

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "multipart form required: " + err.Error()})
		return
	}
	files := form.File["file"]
	if len(files) == 0 {
		files = form.File["files"]
	}
	if len(files) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no files (field 'file')"})
		return
	}
	tsVals := form.Value["ts_ms"] // optional, aligned by index

	created := make([]Screenshot, 0, len(files))
	for i, fh := range files {
		fr, err := fh.Open()
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "open file: " + err.Error()})
			return
		}
		data, err := io.ReadAll(fr)
		fr.Close()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "read file: " + err.Error()})
			return
		}
		mimeType := fh.Header.Get("Content-Type")
		ext := filepath.Ext(fh.Filename)
		scID := newID("sc_")
		meta, err := p.Dock.AssetUpload(sdk.AssetUploadInput{
			WorkspaceID: &wsID,
			Kind:        "media",
			Name:        "film/screenshots/" + mediaID + "/" + scID + ext,
			Version:     "v1",
			Visibility:  "workspace",
			Mime:        mimeType,
			Metadata:    map[string]any{"media_id": mediaID, "kind": "screenshot"},
		}, bytes.NewReader(data))
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "asset upload: " + err.Error()})
			return
		}
		s := Screenshot{
			ID: scID, WorkspaceID: wsID, MediaID: mediaID,
			AssetID: strconv.FormatInt(meta.ID, 10), Phash: computePHash(data),
		}
		if i < len(tsVals) {
			if v, e := strconv.Atoi(strings.TrimSpace(tsVals[i])); e == nil {
				s.TsMs = &v
			}
		}
		if err := p.insertScreenshot(ctx, s); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "cache insert (asset uploaded): " + err.Error()})
			return
		}
		created = append(created, s)
	}
	jsonLen(c, http.StatusCreated, gin.H{"screenshots": created})
}

func (p *Plugin) handleScreenshotList(c *gin.Context) {
	// Paginated: ?limit (default 60, capped 200) & ?offset (>=0). Both default
	// to a sane first page when absent/invalid, so old callers still work.
	limit, offset := parsePageParams(c.Query("limit"), c.Query("offset"))
	shots, total, err := p.listScreenshotsPage(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("id")), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	jsonLen(c, http.StatusOK, gin.H{"screenshots": shots, "total": total, "limit": limit, "offset": offset})
}

// handleScreenshotURL resolves a screenshot's asset_id to a short-lived
// dock-signed provider URL (bytes stream straight from the provider).
func (p *Plugin) handleScreenshotURL(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	s, err := p.getScreenshot(ctx, wsID, strings.TrimSpace(c.Param("scId")))
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "screenshot not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	assetID, err := strconv.ParseInt(s.AssetID, 10, 64)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "bad asset_id"})
		return
	}
	url, err := p.Dock.AssetDownloadURLWS(assetID, wsID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "sign url: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}

func (p *Plugin) handleScreenshotDelete(c *gin.Context) {
	ok, err := p.deleteScreenshot(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("scId")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "screenshot not found"})
		return
	}
	// Note: the asset blob is left in place (may be deduped/shared); assets GC owns it.
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}
