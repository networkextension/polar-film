// Command film-svc is the polar video/film knowledge-base plugin binary.
// M0 skeleton — platform wiring only (auth, heartbeat, healthz, metrics,
// schema). Movie/subtitle/screenshot/AI logic lands in M1+ (doc/dev-plan.md).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	_ "github.com/lib/pq"

	"github.com/networkextension/polar-film/internal/film"
)

func main() {
	cfg := film.Config{
		DBDSN:         envOrDefault("POLAR_FILM_DB_DSN", "postgres://ideamesh:test123456@127.0.0.1:5432/polar_film?sslmode=disable"),
		DockBase:      envOrDefault("POLAR_DOCK_BASE", "http://127.0.0.1:8080"),
		PluginName:    envOrDefault("POLAR_PLUGIN_NAME", "film"),
		PluginToken:   os.Getenv("POLAR_PLUGIN_TOKEN"),
		Listen:        envOrDefault("POLAR_FILM_LISTEN", "127.0.0.1:8102"),
		BuildVersion:  envOrDefault("POLAR_FILM_VERSION", "0.0.1"),
		MetricsToken:  os.Getenv("POLAR_FILM_METRICS_TOKEN"),
		PublicBaseURL: os.Getenv("POLAR_FILM_PUBLIC_BASE_URL"),
	}
	if strings.TrimSpace(cfg.PluginToken) == "" {
		log.Fatal("POLAR_PLUGIN_TOKEN unset — get plaintext from /admin-plugins.html")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	plugin, err := film.New(ctx, cfg)
	if err != nil {
		log.Fatalf("film.New: %v", err)
	}
	defer plugin.Close()

	gin.SetMode(envOrDefault("GIN_MODE", gin.ReleaseMode))
	r := gin.New()
	r.Use(gin.Recovery())
	plugin.RegisterRoutes(r)
	plugin.Start(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: r, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("film-svc listening on %s (dock=%s, name=%s, ver=%s)",
			cfg.Listen, cfg.DockBase, cfg.PluginName, cfg.BuildVersion)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("ListenAndServe: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("film-svc: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("film-svc: shutdown: %v", err)
	}
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
