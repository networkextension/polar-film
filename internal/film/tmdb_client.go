package film

// tmdb_client.go — minimal TMDB (themoviedb.org) v3 REST client used by the M9
// metadata enrichment. Auth is the v4 "API Read Access Token" sent as a Bearer
// (works on the /3 endpoints). One movie fetch pulls details + credits in a
// single call via append_to_response=credits. TMDB is a fixed public API, so no
// SSRF surface; we still bound every call with a context + 15s client timeout,
// mirroring embed.go's http pattern.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TMDB CDN sizing. Backdrops are wide; posters are tall.
const (
	tmdbImageBase    = "https://image.tmdb.org/t/p"
	tmdbBackdropSize = "w1280"
	tmdbPosterSize   = "w500"
	tmdbProfileSize  = "w185"
)

type tmdbClient struct {
	baseURL string
	token   string
	lang    string
	hc      *http.Client
}

func newTMDBClient(baseURL, token string) *tmdbClient {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.themoviedb.org/3"
	}
	return &tmdbClient{
		baseURL: base,
		token:   strings.TrimSpace(token),
		lang:    "zh-CN", // prefer Chinese title/overview; TMDB falls back to original
		hc:      &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *tmdbClient) enabled() bool { return c != nil && c.token != "" }

// tmdbImageURL builds a full CDN URL from a TMDB path ("/abc.jpg"). "" → "".
func tmdbImageURL(size, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return tmdbImageBase + "/" + size + path
}

// tmdbMovie is the subset of /movie/{id}?append_to_response=credits we use.
type tmdbMovie struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate  string  `json:"release_date"` // "2007-05-23" or ""
	VoteAverage  float64 `json:"vote_average"`
	BackdropPath string  `json:"backdrop_path"`
	PosterPath   string  `json:"poster_path"`
	Tagline      string  `json:"tagline"`
	Overview     string  `json:"overview"`
	Runtime      int     `json:"runtime"`
	Credits      struct {
		Cast []struct {
			Name      string `json:"name"`
			Character string `json:"character"`
			Order     int    `json:"order"`
			Profile   string `json:"profile_path"`
		} `json:"cast"`
	} `json:"credits"`
}

func (c *tmdbClient) get(ctx context.Context, path string, q url.Values, out any) error {
	if !c.enabled() {
		return errors.New("tmdb: not configured")
	}
	if q == nil {
		q = url.Values{}
	}
	if q.Get("language") == "" {
		q.Set("language", c.lang)
	}
	u := c.baseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// searchMovie resolves a (title, year) to a TMDB id. year<=0 skips the filter.
// Returns 0 + nil when nothing matched.
func (c *tmdbClient) searchMovie(ctx context.Context, title string, year int) (int, error) {
	q := url.Values{}
	q.Set("query", title)
	q.Set("include_adult", "false")
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var res struct {
		Results []struct {
			ID int `json:"id"`
		} `json:"results"`
	}
	if err := c.get(ctx, "/search/movie", q, &res); err != nil {
		return 0, err
	}
	if len(res.Results) == 0 {
		return 0, nil
	}
	return res.Results[0].ID, nil
}

// fetchMovie pulls full details + credits for a TMDB id.
func (c *tmdbClient) fetchMovie(ctx context.Context, tmdbID int) (*tmdbMovie, error) {
	q := url.Values{}
	q.Set("append_to_response", "credits")
	var m tmdbMovie
	if err := c.get(ctx, "/movie/"+strconv.Itoa(tmdbID), q, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
