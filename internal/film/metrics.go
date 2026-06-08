package film

import (
	"context"
	"database/sql"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// filmMetrics holds the plugin's private Prometheus registry, served at
// /metrics behind POLAR_FILM_METRICS_TOKEN. Liveness gauge + domain
// counters (search/analyze/embed) + table-size gauges refreshed each
// heartbeat.
type filmMetrics struct {
	registry *prometheus.Registry
	upGauge  prometheus.Gauge

	searchTotal  *prometheus.CounterVec // by mode (keyword|semantic)
	analyzeTotal *prometheus.CounterVec // by result (done|failed)
	embedTotal   *prometheus.CounterVec // by kind (segment|movie)

	rows *prometheus.GaugeVec // table row counts by table
}

func newFilmMetrics() *filmMetrics {
	m := &filmMetrics{registry: prometheus.NewRegistry()}
	m.upGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "polar_film_up",
		Help: "Always 1 while film-svc is serving.",
	})
	m.searchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "polar_film_search_total",
		Help: "Search requests by mode.",
	}, []string{"mode"})
	m.analyzeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "polar_film_analyze_jobs_total",
		Help: "Completed analyze jobs by terminal result.",
	}, []string{"result"})
	m.embedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "polar_film_embeddings_total",
		Help: "Embeddings written by kind.",
	}, []string{"kind"})
	m.rows = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "polar_film_rows",
		Help: "Row counts of key film tables.",
	}, []string{"table"})
	m.registry.MustRegister(m.upGauge, m.searchTotal, m.analyzeTotal, m.embedTotal, m.rows)
	m.upGauge.Set(1)
	return m
}

// nil-safe increment helpers (tests build Plugin without metrics).
func (m *filmMetrics) incSearch(mode string) {
	if m != nil {
		m.searchTotal.WithLabelValues(mode).Inc()
	}
}
func (m *filmMetrics) incAnalyze(result string) {
	if m != nil {
		m.analyzeTotal.WithLabelValues(result).Inc()
	}
}
func (m *filmMetrics) addEmbed(kind string, n int) {
	if m != nil && n > 0 {
		m.embedTotal.WithLabelValues(kind).Add(float64(n))
	}
}

// refreshRowGauges reruns the table COUNTs. Called on each heartbeat tick;
// dev-scale tables make a per-minute COUNT(*) cheap. Best-effort.
func (m *filmMetrics) refreshRowGauges(ctx context.Context, db *sql.DB) {
	if m == nil || db == nil {
		return
	}
	tables := []string{"media_items", "subtitle_segments", "screenshots", "media_embeddings", "analyze_jobs"}
	for _, t := range tables {
		var n int64
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		// table names are internal constants, never user input
		err := db.QueryRowContext(cctx, "SELECT count(*) FROM "+t).Scan(&n)
		cancel()
		if err == nil {
			m.rows.WithLabelValues(t).Set(float64(n))
		}
	}
}
