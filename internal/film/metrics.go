package film

import "github.com/prometheus/client_golang/prometheus"

// filmMetrics holds the plugin's private Prometheus registry, served at
// /metrics behind POLAR_FILM_METRICS_TOKEN. M0 ships only a liveness gauge;
// ingest/analyze/search counters arrive with their features.
type filmMetrics struct {
	registry *prometheus.Registry
	upGauge  prometheus.Gauge
}

func newFilmMetrics() *filmMetrics {
	m := &filmMetrics{registry: prometheus.NewRegistry()}
	m.upGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "polar_film_up",
		Help: "Always 1 while film-svc is serving.",
	})
	m.registry.MustRegister(m.upGauge)
	m.upGauge.Set(1)
	return m
}
