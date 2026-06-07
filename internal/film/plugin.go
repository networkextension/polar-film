// Package film is the polar video/film knowledge-base plugin: movie
// metadata + segmented subtitles + screenshots + people + timeline + tags
// + (later) embeddings. Philosophy: store the knowledge structure, not the
// video — binaries live in polar-assets. See doc/dev-plan.md.
//
// Like every polar plugin it owns its own database (polar_film), validates
// user sessions through dock's /internal/v1/auth/verify (via polar-sdk),
// and heartbeats into dock's plugin registry. M0 is platform wiring only.
package film

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/networkextension/polar-sdk"
)

type Plugin struct {
	DB         *sql.DB
	Dock       *sdk.Client
	Name       string
	Listen     string
	Ver        string
	MetricsTok string
	PublicURL  string // externally reachable origin, sent on heartbeat

	metrics   *filmMetrics
	startedAt time.Time
}

func New(ctx context.Context, cfg Config) (*Plugin, error) {
	cfg.PluginName = strings.TrimSpace(cfg.PluginName)
	if cfg.PluginName == "" {
		cfg.PluginName = "film"
	}
	if strings.TrimSpace(cfg.DBDSN) == "" {
		return nil, errors.New("film.New: DBDSN required")
	}
	if strings.TrimSpace(cfg.DockBase) == "" {
		return nil, errors.New("film.New: DockBase required")
	}
	if strings.TrimSpace(cfg.PluginToken) == "" {
		return nil, errors.New("film.New: PluginToken required")
	}

	db, err := sql.Open("postgres", cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open polar_film: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping polar_film: %w", err)
	}

	dock := sdk.NewClient(cfg.DockBase, cfg.PluginName, sdk.DeriveHMACKey(cfg.PluginToken))
	resp, err := dock.Do(http.MethodGet, "/internal/v1/ping", nil)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dock ping: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = db.Close()
		return nil, fmt.Errorf("dock /ping rejected: HTTP %d", resp.StatusCode)
	}

	return &Plugin{
		DB:         db,
		Dock:       dock,
		Name:       cfg.PluginName,
		Listen:     cfg.Listen,
		Ver:        cfg.BuildVersion,
		MetricsTok: cfg.MetricsToken,
		PublicURL:  strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
		metrics:    newFilmMetrics(),
		startedAt:  time.Now(),
	}, nil
}

func (p *Plugin) RegisterRoutes(r gin.IRouter) {
	r.GET("/healthz", p.handleHealthz)
	r.GET("/metrics", p.handleMetricsExposition)

	// /api/film/* — user API. nginx proxies /api/film/* → film-svc. M0 carries
	// only a _whoami probe to prove the auth chain; movies/people/subtitles/
	// screenshots/search/analyze land in M1+ under this same group.
	api := r.Group("/api/film")
	{
		auth := api.Group("", p.requireAuthViaDock())
		{
			auth.GET("/_whoami", p.handleWhoami)

			// Movies (media_items).
			auth.POST("/movies", p.handleMovieCreate)
			auth.GET("/movies", p.handleMovieList)
			auth.GET("/movies/:id", p.handleMovieDetail)
			auth.PATCH("/movies/:id", p.handleMovieUpdate)
			auth.DELETE("/movies/:id", p.handleMovieDelete)

			// People + cast links.
			auth.POST("/people", p.handlePersonCreate)
			auth.GET("/people", p.handlePersonList)
			auth.POST("/movies/:id/people", p.handleMoviePersonAttach)
			auth.DELETE("/movies/:id/people/:personId/:role", p.handleMoviePersonDetach)

			// Tags + links.
			auth.POST("/tags", p.handleTagCreate)
			auth.GET("/tags", p.handleTagList)
			auth.POST("/movies/:id/tags", p.handleMovieTagAttach)
			auth.DELETE("/movies/:id/tags/:tagId", p.handleMovieTagDetach)

			// Subtitles (upload → SRT/VTT parsed into segments) + 台词 search.
			auth.POST("/movies/:id/subtitles", p.handleSubtitleUpload)
			auth.GET("/movies/:id/subtitles", p.handleSubtitleList)
			auth.GET("/subtitles/:subId/segments", p.handleSubtitleSegments)
			auth.DELETE("/subtitles/:subId", p.handleSubtitleDelete)
			auth.GET("/search", p.handleSearch)

			// Screenshots (binary → polar-assets via SDK; row holds asset_id + phash).
			auth.POST("/movies/:id/screenshots", p.handleScreenshotUpload)
			auth.GET("/movies/:id/screenshots", p.handleScreenshotList)
			auth.GET("/screenshots/:scId/url", p.handleScreenshotURL)
			auth.DELETE("/screenshots/:scId", p.handleScreenshotDelete)
		}
	}
}

func (p *Plugin) Start(ctx context.Context) {
	go p.heartbeatLoop(ctx)
}

func (p *Plugin) Close() error {
	if p.DB != nil {
		return p.DB.Close()
	}
	return nil
}

func (p *Plugin) handleHealthz(c *gin.Context) {
	dbOK := true
	if err := p.DB.PingContext(c.Request.Context()); err != nil {
		dbOK = false
	}
	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{
		"plugin":         p.Name,
		"version":        p.Ver,
		"uptime_seconds": int64(time.Since(p.startedAt).Seconds()),
		"db_ok":          dbOK,
		"go":             runtime.Version(),
	})
}

func (p *Plugin) handleMetricsExposition(c *gin.Context) {
	if p.MetricsTok == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if c.GetHeader("Authorization") != "Bearer "+p.MetricsTok {
		c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	promhttp.HandlerFor(p.metrics.registry, promhttp.HandlerOpts{}).ServeHTTP(c.Writer, c.Request)
}

// handleWhoami echoes the resolved identity — M0 probe that the
// AuthVerify + workspace-access middleware chain works end to end.
func (p *Plugin) handleWhoami(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"user_id":      c.GetString(ctxKeyUserID),
		"user_role":    c.GetString(ctxKeyUserRole),
		"workspace_id": c.GetString(ctxKeyWorkspaceID),
	})
}

func (p *Plugin) heartbeatLoop(ctx context.Context) {
	p.beat(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.beat(ctx)
		}
	}
}

// filmUIRoutes — sidebar entry this plugin contributes. Path is the root of
// the module's own UI (M6); dock joins it with PublicBaseURL for the
// cross-subdomain sidebar link via /api/nav.
var filmUIRoutes = []sdk.UIRoute{
	{Path: "/", Label: "影库", Icon: "film", Order: 60},
}

func (p *Plugin) beat(_ context.Context) {
	err := p.Dock.Heartbeat(sdk.HeartbeatOpts{
		Version:       p.Ver,
		Endpoint:      p.Listen,
		UptimeSeconds: int64(time.Since(p.startedAt).Seconds()),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		UIRoutes:      filmUIRoutes,
		PublicBaseURL: p.PublicURL,
	})
	if err != nil {
		// best-effort; dock may be briefly unavailable
		log.Printf("film: heartbeat failed: %v", err)
	}
}
