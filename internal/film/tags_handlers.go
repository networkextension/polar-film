package film

// tags_handlers.go — /api/film/tags CRUD + attach/detach to movies.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) handleTagCreate(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	var req struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	kind := strings.TrimSpace(req.Kind)
	if kind == "" {
		kind = "genre"
	}
	t := Tag{ID: newID("tg_"), WorkspaceID: wsID, Name: req.Name, Kind: kind}
	if err := p.insertTag(c.Request.Context(), t); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "tag " + req.Name + " already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, t)
}

func (p *Plugin) handleTagList(c *gin.Context) {
	tags, err := p.listTags(c.Request.Context(), c.GetString(ctxKeyWorkspaceID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

// handleMovieTagAttach attaches a tag by tag_id, or by name (created on the
// fly via ensureTag when no id is given).
func (p *Plugin) handleMovieTagAttach(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))

	var req struct {
		TagID  string `json:"tag_id"`
		Name   string `json:"name"`
		Kind   string `json:"kind"`
		Source string `json:"source"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	if _, err := p.getMovie(ctx, wsID, mediaID); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	tagID := strings.TrimSpace(req.TagID)
	if tagID == "" {
		name := strings.TrimSpace(req.Name)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "tag_id or name is required"})
			return
		}
		var err error
		if tagID, err = p.ensureTag(ctx, wsID, name, req.Kind); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else if !p.existsInWorkspace(ctx, "tags", wsID, tagID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tag not found in this workspace"})
		return
	}

	if err := p.attachTag(ctx, wsID, mediaID, tagID, req.Source); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	tags, _ := p.listMovieTags(ctx, wsID, mediaID)
	c.JSON(http.StatusOK, gin.H{"tags": tags})
}

func (p *Plugin) handleMovieTagDetach(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	ok, err := p.detachTag(ctx, wsID, strings.TrimSpace(c.Param("id")), strings.TrimSpace(c.Param("tagId")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag link not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detached": true})
}
