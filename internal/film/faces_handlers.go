package film

// faces_handlers.go — upload + read face clusters (M9, face curation P0).
//
//   POST /api/film/movies/:id/faces          {clusters:[…], faces:[…]}  (replace)
//   GET  /api/film/movies/:id/face-clusters
//   GET  /api/film/face-clusters/:cid/faces
//
// Faces reference an already-uploaded keyframe by ts_ms (keyframes carry the
// frame's timeMs). Curation ops (merge/remove/split/assign) are P1.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type faceUploadCluster struct {
	Label   string  `json:"label"`
	RepTsMs *int    `json:"rep_ts_ms"`
	RepBox  Box     `json:"rep_box"`
	Conf    float64 `json:"conf"`
}

type faceUploadFace struct {
	Label   string  `json:"label"`
	TsMs    *int    `json:"ts_ms"`
	Box     Box     `json:"box"`
	Quality float64 `json:"quality"`
}

func (p *Plugin) handleFacesUpload(c *gin.Context) {
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
		Clusters []faceUploadCluster `json:"clusters"`
		Faces    []faceUploadFace    `json:"faces"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	// ts_ms → screenshot_id, so faces/cluster reps point at the real keyframes.
	shots, err := p.listScreenshots(ctx, wsID, mediaID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	scByTs := make(map[int]string, len(shots))
	for _, s := range shots {
		if s.TsMs != nil {
			scByTs[*s.TsMs] = s.ID
		}
	}
	rep := func(ts *int) string {
		if ts == nil {
			return ""
		}
		return scByTs[*ts]
	}

	// One face_clusters row per distinct label (from the clusters[] list and any
	// labels seen on faces). Mint stable ids keyed by label so faces can link.
	idByLabel := map[string]string{}
	clusters := make([]FaceCluster, 0, len(req.Clusters))
	ensure := func(label string) string {
		if id, ok := idByLabel[label]; ok {
			return id
		}
		id := newID("fc_")
		idByLabel[label] = id
		clusters = append(clusters, FaceCluster{ID: id, Label: label, Source: "filmscan"})
		return id
	}
	clusterAt := map[string]int{} // label → index into clusters
	for _, rc := range req.Clusters {
		id := ensure(rc.Label)
		idx := -1
		for i := range clusters {
			if clusters[i].ID == id {
				idx = i
				break
			}
		}
		clusterAt[rc.Label] = idx
		clusters[idx].RepScreenshotID = rep(rc.RepTsMs)
		clusters[idx].RepBox = rc.RepBox
		clusters[idx].Conf = rc.Conf
	}

	faces := make([]Face, 0, len(req.Faces))
	count := map[string]int{}
	for _, rf := range req.Faces {
		label := rf.Label
		cid := ensure(label)
		count[label]++
		faces = append(faces, Face{
			ID:           newID("fa_"),
			ClusterID:    cid,
			ScreenshotID: rep(rf.TsMs),
			TsMs:         rf.TsMs,
			Box:          rf.Box,
			Quality:      rf.Quality,
		})
	}
	// face_count per cluster; backfill rep for clusters that only came from faces.
	for i := range clusters {
		clusters[i].FaceCount = count[clusters[i].Label]
		if clusters[i].RepScreenshotID == "" {
			for _, f := range faces {
				if f.ClusterID == clusters[i].ID {
					clusters[i].RepScreenshotID = f.ScreenshotID
					clusters[i].RepBox = f.Box
					break
				}
			}
		}
	}

	if err := p.replaceMovieFaces(ctx, wsID, mediaID, clusters, faces); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "store faces: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"clusters": len(clusters), "faces": len(faces)})
}

func (p *Plugin) handleFaceClusterList(c *gin.Context) {
	clusters, err := p.listFaceClusters(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("id")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"clusters": clusters})
}

func (p *Plugin) handleFaceClusterFaces(c *gin.Context) {
	faces, err := p.listClusterFaces(c.Request.Context(), c.GetString(ctxKeyWorkspaceID), strings.TrimSpace(c.Param("cid")))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"faces": faces})
}
