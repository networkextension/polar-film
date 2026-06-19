package film

// face_search_handlers.go — face re-identification endpoints (PF-14):
//
//   GET /api/film/movies/:id/face-suggestions?max_dist=0.35   疑似同人 cluster pairs
//   GET /api/film/movies/:id/faces/:faceId/similar?k=12       within-movie similar faces
//   GET /api/film/people/:id/cross-film?k=24&exclude_media=…  cross-film person search
//
// All read the per-face Vision feature-print in pgvector. They are curation
// *assists* (whole-crop features, conservative thresholds), not authoritative
// identity. See doc/face-curation.md.

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// FaceRep is a cluster's representative crop (keyframe + normalized box).
type FaceRep struct {
	ScreenshotID string `json:"screenshot_id"`
	Box          Box    `json:"box"`
}

// FaceSuggestion is an enriched疑似同人 pair for the UI.
type FaceSuggestion struct {
	ClusterA string  `json:"cluster_a"`
	ClusterB string  `json:"cluster_b"`
	Distance float64 `json:"distance"`
	Score    float64 `json:"score"`
	LabelA   string  `json:"label_a"`
	LabelB   string  `json:"label_b"`
	PersonA  string  `json:"person_a,omitempty"`
	PersonB  string  `json:"person_b,omitempty"`
	CountA   int     `json:"count_a"`
	CountB   int     `json:"count_b"`
	RepA     FaceRep `json:"rep_a"`
	RepB     FaceRep `json:"rep_b"`
}

func (p *Plugin) handleFaceSuggestions(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))
	if !p.movieOK(c, wsID, mediaID) {
		return
	}
	maxDist := 0.35
	if v := strings.TrimSpace(c.Query("max_dist")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 2 {
			maxDist = f
		}
	}
	pairs, err := p.faceClusterCentroidPairs(ctx, wsID, mediaID, maxDist, 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	// activate the (formerly dead) face_merge_suggestions cache; best-effort.
	_ = p.persistSuggestions(ctx, mediaID, pairs)

	clusters, err := p.listFaceClusters(ctx, wsID, mediaID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	byID := make(map[string]FaceCluster, len(clusters))
	for _, cl := range clusters {
		byID[cl.ID] = cl
	}
	out := make([]FaceSuggestion, 0, len(pairs))
	for _, pr := range pairs {
		a, b := byID[pr.A], byID[pr.B]
		out = append(out, FaceSuggestion{
			ClusterA: pr.A, ClusterB: pr.B,
			Distance: pr.Dist, Score: 1 - pr.Dist,
			LabelA: a.Label, LabelB: b.Label,
			PersonA: a.PersonName, PersonB: b.PersonName,
			CountA: a.FaceCount, CountB: b.FaceCount,
			RepA: FaceRep{ScreenshotID: a.RepScreenshotID, Box: a.RepBox},
			RepB: FaceRep{ScreenshotID: b.RepScreenshotID, Box: b.RepBox},
		})
	}
	c.JSON(http.StatusOK, gin.H{"suggestions": out, "max_dist": maxDist})
}

func (p *Plugin) handleFaceSimilar(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	mediaID := strings.TrimSpace(c.Param("id"))
	faceID := strings.TrimSpace(c.Param("faceId"))
	if !p.movieOK(c, wsID, mediaID) {
		return
	}
	k := atoiDefault(c.Query("k"), 12)
	faces, err := p.similarFacesInMovie(ctx, wsID, mediaID, faceID, k)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"faces": faces})
}

func (p *Plugin) handlePersonCrossFilm(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	personID := strings.TrimSpace(c.Param("id"))
	name, ok, err := p.getPersonName(ctx, wsID, personID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "person not found"})
		return
	}
	k := atoiDefault(c.Query("k"), 24)
	movies, err := p.crossFilmPersonFaces(ctx, wsID, personID, strings.TrimSpace(c.Query("exclude_media")), k)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"person": gin.H{"id": personID, "name": name},
		"movies": movies,
	})
}

// atoiDefault parses a query int, falling back to def on empty/invalid.
func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 {
		return n
	}
	return def
}
