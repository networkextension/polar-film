package film

// people_handlers.go — /api/film/people CRUD + attach/detach to movies.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) handlePersonCreate(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	var req struct {
		Name          string `json:"name"`
		AvatarAssetID string `json:"avatar_asset_id"`
		Bio           string `json:"bio"`
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
	ps := Person{ID: newID("pe_"), WorkspaceID: wsID, Name: req.Name, AvatarAssetID: req.AvatarAssetID, Bio: req.Bio}
	if err := p.insertPerson(c.Request.Context(), ps); err != nil {
		if isUniqueViolation(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "a person named " + req.Name + " already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, ps)
}

func (p *Plugin) handlePersonList(c *gin.Context) {
	people, err := p.listPeople(c.Request.Context(), c.GetString(ctxKeyWorkspaceID))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"people": people})
}

func (p *Plugin) handleMoviePersonAttach(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))

	var req struct {
		PersonID  string `json:"person_id"`
		Role      string `json:"role"`
		Character string `json:"character"`
		Ord       int    `json:"ord"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.PersonID = strings.TrimSpace(req.PersonID)
	req.Role = strings.TrimSpace(req.Role)
	if req.PersonID == "" || req.Role == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "person_id and role are required"})
		return
	}
	if _, err := p.getMovie(ctx, wsID, mediaID); errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "movie not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !p.existsInWorkspace(ctx, "people", wsID, req.PersonID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "person not found in this workspace"})
		return
	}
	if err := p.attachPerson(ctx, wsID, mediaID, req.PersonID, req.Role, req.Character, req.Ord); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	cast, _ := p.listMoviePeople(ctx, wsID, mediaID)
	c.JSON(http.StatusOK, gin.H{"cast": cast})
}

func (p *Plugin) handleMoviePersonDetach(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	ok, err := p.detachPerson(ctx, wsID, strings.TrimSpace(c.Param("id")),
		strings.TrimSpace(c.Param("personId")), strings.TrimSpace(c.Param("role")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "cast link not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"detached": true})
}
