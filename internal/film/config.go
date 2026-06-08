package film

// Config is the film-svc boot configuration, sourced from env in
// cmd/film-svc/main.go. Kept small for the M0 skeleton; provider/model
// settings (embedding, assets) arrive with their milestones.
type Config struct {
	DBDSN         string // POLAR_FILM_DB_DSN — connects ONLY to polar_film
	DockBase      string // POLAR_DOCK_BASE — e.g. http://127.0.0.1:8080
	PluginName    string // POLAR_PLUGIN_NAME — must match plugin_modules.name in dock
	PluginToken   string // POLAR_PLUGIN_TOKEN — plaintext shown once by dock admin
	Listen        string // POLAR_FILM_LISTEN — e.g. 127.0.0.1:8102
	BuildVersion  string // POLAR_FILM_VERSION
	MetricsToken  string // POLAR_FILM_METRICS_TOKEN — Bearer for /metrics (empty → 404)
	PublicBaseURL string // POLAR_FILM_PUBLIC_BASE_URL — origin for dock /api/nav sidebar link

	// Embedding backend (M4). Empty BaseURL → deterministic offline
	// fallback (non-semantic). Dev uses ollama: BaseURL=http://127.0.0.1:11434/v1,
	// Model=bge-m3, Dim=1024, APIKey empty. DashScope/OpenAI work the same way.
	EmbedBaseURL string // POLAR_FILM_EMBED_BASE_URL — OpenAI-compatible /v1 base
	EmbedModel   string // POLAR_FILM_EMBED_MODEL — e.g. bge-m3
	EmbedDim     int    // POLAR_FILM_EMBED_DIM — must match schema vector(N); default 1024
	EmbedAPIKey  string // POLAR_FILM_EMBED_API_KEY — optional (ollama ignores)
}
