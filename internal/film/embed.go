package film

// embed.go — the embedding backend behind M4 semantic search. The
// Embedder interface is the seam: dev runs ollama bge-m3 (1024-dim,
// Chinese-strong, zero API key) via the OpenAI-compatible httpEmbedder,
// but DashScope / OpenAI / a future dock embedding endpoint all speak the
// same /v1/embeddings shape, so swapping backends is a config change.
//
// When no backend is configured (or it's unreachable) we fall back to a
// deterministic hashEmbedder: NOT semantic, but it keeps the pgvector
// write/query path exercised offline and in tests. Real deployments set
// POLAR_FILM_EMBED_BASE_URL.

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Embedder turns text into fixed-dimension unit vectors. Embed is batch:
// callers SHOULD pass many texts per call (subtitle segments, etc.) so
// the backend can amortize round-trips.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dim() int
	Name() string
}

// newEmbedder picks a backend from config. A base URL → OpenAI-compatible
// HTTP backend; empty → the deterministic offline fallback.
func newEmbedder(cfg Config) Embedder {
	dim := cfg.EmbedDim
	if dim <= 0 {
		dim = 1024
	}
	base := strings.TrimRight(strings.TrimSpace(cfg.EmbedBaseURL), "/")
	if base == "" {
		return &hashEmbedder{dim: dim}
	}
	model := strings.TrimSpace(cfg.EmbedModel)
	if model == "" {
		model = "bge-m3"
	}
	return &httpEmbedder{
		base:   base,
		model:  model,
		apiKey: strings.TrimSpace(cfg.EmbedAPIKey),
		dim:    dim,
		hc:     &http.Client{Timeout: 60 * time.Second},
	}
}

// ── OpenAI-compatible HTTP backend (ollama / DashScope / OpenAI) ──────

type httpEmbedder struct {
	base   string // e.g. http://127.0.0.1:11434/v1
	model  string // e.g. bge-m3
	apiKey string // optional (ollama ignores it)
	dim    int
	hc     *http.Client
}

func (e *httpEmbedder) Dim() int     { return e.dim }
func (e *httpEmbedder) Name() string { return "http:" + e.model }

type embeddingsReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingsResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (e *httpEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, _ := json.Marshal(embeddingsReq{Model: e.model, Input: texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out embeddingsResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("embeddings error: %s", out.Error.Message)
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings count mismatch: got %d want %d", len(out.Data), len(texts))
	}
	vecs := make([][]float32, len(out.Data))
	for i := range out.Data {
		v := out.Data[i].Embedding
		if len(v) != e.dim {
			return nil, fmt.Errorf("embedding dim mismatch: got %d want %d (model %s)", len(v), e.dim, e.model)
		}
		vecs[i] = normalize(v)
	}
	return vecs, nil
}

// ── deterministic offline fallback (NOT semantic) ────────────────────

type hashEmbedder struct{ dim int }

func (e *hashEmbedder) Dim() int     { return e.dim }
func (e *hashEmbedder) Name() string { return "hash(offline)" }

// Embed maps tokens into a bag-of-hashed-features vector. Same text →
// same vector; lexical overlap → some cosine similarity. Good enough to
// validate the vector pipeline; useless for real semantics.
//
// Tokenization is rune-level + adjacent-rune bigrams so it works for CJK
// (no word spaces) as well as space-delimited scripts — whitespace
// splitting alone would make every Chinese line a single token and kill
// all overlap.
func (e *hashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, e.dim)
		for _, tok := range hashTokens(t) {
			h := sha1.Sum([]byte(tok))
			bucket := int(binary.BigEndian.Uint32(h[0:4])) % e.dim
			if bucket < 0 {
				bucket += e.dim
			}
			sign := float32(1)
			if h[4]&1 == 1 {
				sign = -1
			}
			v[bucket] += sign
		}
		out[i] = normalize(v)
	}
	return out, nil
}

// hashTokens lowercases, drops punctuation/whitespace, then emits each
// rune plus every adjacent-rune bigram. Bigrams give CJK enough locality
// to separate related from unrelated text.
func hashTokens(t string) []string {
	var runes []rune
	for _, r := range strings.ToLower(t) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			runes = append(runes, r)
		}
	}
	if len(runes) == 0 {
		return nil
	}
	toks := make([]string, 0, len(runes)*2)
	for i, r := range runes {
		toks = append(toks, string(r))
		if i+1 < len(runes) {
			toks = append(toks, string(runes[i:i+2]))
		}
	}
	return toks
}

// ── shared helpers ───────────────────────────────────────────────────

// normalize returns the L2-normalized vector so cosine distance (`<=>`)
// behaves like a dot product. A zero vector is returned unchanged.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x * inv
	}
	return out
}

// vectorLiteral formats a float32 slice as a pgvector text literal,
// e.g. "[0.1,0.2,0.3]", for binding as $n::vector. Compact, no spaces.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
