package film

// screenshots_direct_handlers.go — direct-to-storage keyframe upload.
//
//   POST /api/film/movies/:id/screenshots/grants   {items:[{sha256,size,mime,ts_ms}]}
//   POST /api/film/movies/:id/screenshots/commit   {items:[{screenshot_id,asset_id,provider_id,ts_ms,phash,exists}]}
//
// Unlike the legacy multipart POST /screenshots (which streams every keyframe
// through film-svc → SDK AssetUpload → provider), this lets the CLIENT PUT
// bytes straight to the assets provider:
//
//   1. client hashes each keyframe → POST /grants → film mints a content-
//      addressed upload grant per item (asset_id + put_url, or exists=true);
//   2. client PUTs bytes to put_url (provider-direct) for the misses;
//   3. client → POST /commit → film finalizes each new blob + records the
//      screenshots row (asset_id, ts_ms, phash). Idempotent.
//
// film-svc never sees the keyframe bytes. phash is computed client-side and
// passed in commit (must match phash.go's dHash so cross-dedup holds).

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	sdk "github.com/networkextension/polar-sdk"
)

const screenshotGrantBatchMax = 500

type screenshotGrantReqItem struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mime   string `json:"mime"`
	Ext    string `json:"ext"` // e.g. ".jpg"; used only to name the asset
	TsMs   *int   `json:"ts_ms"`
}

type screenshotGrantRespItem struct {
	SHA256       string `json:"sha256"`
	ScreenshotID string `json:"screenshot_id"`
	AssetID      int64  `json:"asset_id"`
	ProviderID   int64  `json:"provider_id"`
	PutURL       string `json:"put_url"`
	Exists       bool   `json:"exists"`
}

// handleScreenshotGrants mints one upload grant per requested keyframe.
func (p *Plugin) handleScreenshotGrants(c *gin.Context) {
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

	var req struct {
		Items []screenshotGrantReqItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no items"})
		return
	}
	if len(req.Items) > screenshotGrantBatchMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many items (max " + strconv.Itoa(screenshotGrantBatchMax) + ")"})
		return
	}

	grants := make([]screenshotGrantRespItem, 0, len(req.Items))
	for _, it := range req.Items {
		sha := strings.TrimSpace(it.SHA256)
		if sha == "" || it.Size <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each item needs sha256 + positive size"})
			return
		}
		mime := it.Mime
		if mime == "" {
			mime = "image/jpeg"
		}
		ext := strings.TrimSpace(it.Ext)
		if ext == "" {
			ext = ".jpg"
		}
		scID := newID("sc_")
		g, err := p.Dock.AssetUploadGrant(sdk.AssetUploadInput{
			WorkspaceID: &wsID,
			Kind:        "media",
			Name:        "film/screenshots/" + mediaID + "/" + scID + ext,
			Version:     "v1",
			Visibility:  "workspace",
			Mime:        mime,
			Metadata:    map[string]any{"media_id": mediaID, "kind": "screenshot"},
		}, sha, it.Size)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "asset grant: " + err.Error()})
			return
		}
		grants = append(grants, screenshotGrantRespItem{
			SHA256:       sha,
			ScreenshotID: scID,
			AssetID:      g.AssetID,
			ProviderID:   g.ProviderID,
			PutURL:       g.PutURL,
			Exists:       g.Exists,
		})
	}
	c.JSON(http.StatusOK, gin.H{"grants": grants})
}

type screenshotCommitReqItem struct {
	ScreenshotID string `json:"screenshot_id"`
	AssetID      int64  `json:"asset_id"`
	ProviderID   int64  `json:"provider_id"`
	TsMs         *int   `json:"ts_ms"`
	Phash        string `json:"phash"`
	Exists       bool   `json:"exists"`
}

// handleScreenshotCommit finalizes the newly-PUT blobs and records the rows.
func (p *Plugin) handleScreenshotCommit(c *gin.Context) {
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

	var req struct {
		Items []screenshotCommitReqItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if len(req.Items) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no items"})
		return
	}
	if len(req.Items) > screenshotGrantBatchMax {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too many items (max " + strconv.Itoa(screenshotGrantBatchMax) + ")"})
		return
	}

	committed := 0
	for _, it := range req.Items {
		if strings.TrimSpace(it.ScreenshotID) == "" || it.AssetID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each item needs screenshot_id + asset_id"})
			return
		}
		// New content was PUT by the client → mark the blob ready. Deduped
		// content (exists) was already finalized when first uploaded; skip.
		if !it.Exists {
			if _, err := p.Dock.AssetFinalize(it.AssetID, it.ProviderID); err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": "asset finalize: " + err.Error()})
				return
			}
		}
		s := Screenshot{
			ID:          it.ScreenshotID,
			WorkspaceID: wsID,
			MediaID:     mediaID,
			AssetID:     strconv.FormatInt(it.AssetID, 10),
			Phash:       it.Phash,
			TsMs:        it.TsMs,
		}
		if err := p.insertScreenshot(ctx, s); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "record screenshot (asset finalized): " + err.Error()})
			return
		}
		committed++
	}
	c.JSON(http.StatusOK, gin.H{"committed": committed})
}
