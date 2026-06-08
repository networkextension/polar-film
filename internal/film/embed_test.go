package film

import (
	"context"
	"math"
	"strings"
	"testing"
)

func TestNormalizeUnitLength(t *testing.T) {
	v := normalize([]float32{3, 4}) // 3-4-5 triangle → unit
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Fatalf("normalized length² = %v, want 1", sum)
	}
	// zero vector stays zero (no NaN)
	z := normalize([]float32{0, 0, 0})
	for _, x := range z {
		if x != 0 {
			t.Fatalf("zero vector got %v", z)
		}
	}
}

func TestVectorLiteral(t *testing.T) {
	got := vectorLiteral([]float32{0.5, -1, 2})
	if got != "[0.5,-1,2]" {
		t.Fatalf("vectorLiteral = %q, want [0.5,-1,2]", got)
	}
	if vectorLiteral(nil) != "[]" {
		t.Fatalf("empty literal = %q, want []", vectorLiteral(nil))
	}
}

func TestHashEmbedderDeterministicAndDim(t *testing.T) {
	e := &hashEmbedder{dim: 64}
	if e.Dim() != 64 {
		t.Fatalf("dim = %d", e.Dim())
	}
	out, err := e.Embed(context.Background(), []string{"不疯魔 不成活", "不疯魔 不成活", "完全不同 的 句子"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d vecs", len(out))
	}
	for _, v := range out {
		if len(v) != 64 {
			t.Fatalf("vec dim %d", len(v))
		}
	}
	// identical text → identical vector
	if cosine(out[0], out[1]) < 0.9999 {
		t.Fatalf("same text should be identical, cos=%v", cosine(out[0], out[1]))
	}
	// overlapping tokens → more similar than fully disjoint text would be
	// (sanity: self-similarity beats cross-similarity)
	if cosine(out[0], out[2]) >= cosine(out[0], out[1]) {
		t.Fatalf("disjoint text shouldn't beat identical text")
	}
}

func TestNewEmbedderPicksBackend(t *testing.T) {
	if got := newEmbedder(Config{}).Name(); !strings.Contains(got, "hash") {
		t.Fatalf("no base URL should give offline fallback, got %q", got)
	}
	got := newEmbedder(Config{EmbedBaseURL: "http://127.0.0.1:11434/v1", EmbedModel: "bge-m3"})
	if h, ok := got.(*httpEmbedder); !ok || h.model != "bge-m3" || h.dim != 1024 {
		t.Fatalf("base URL should give httpEmbedder bge-m3 dim1024, got %+v", got)
	}
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // inputs are already unit-normalized
}
