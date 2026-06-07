package film

import "testing"

func sp(s string) *string { return &s }
func ip(i int) *int       { return &i }

func TestApplyMoviePatch_NoOpWhenEmpty(t *testing.T) {
	y := 1993
	cur := Movie{ID: "mv_1", Kind: "movie", Title: "霸王别姬", Year: &y, RuntimeMin: ip(171)}
	got := applyMoviePatch(cur, updateMovieReq{})
	if got.Title != "霸王别姬" || got.Kind != "movie" || got.Year == nil || *got.Year != 1993 || got.RuntimeMin == nil || *got.RuntimeMin != 171 {
		t.Fatalf("empty patch should be a no-op: %+v", got)
	}
}

func TestApplyMoviePatch_OverlaysProvided(t *testing.T) {
	cur := Movie{Title: "old", Summary: "", Year: ip(2000)}
	got := applyMoviePatch(cur, updateMovieReq{
		Title:   sp("new"),
		Summary: sp("a tale"),
		Year:    ip(2021),
	})
	if got.Title != "new" || got.Summary != "a tale" || got.Year == nil || *got.Year != 2021 {
		t.Fatalf("provided fields not applied: %+v", got)
	}
}

func TestApplyMoviePatch_UntouchedSurvive(t *testing.T) {
	cur := Movie{ID: "mv_1", Title: "keep", Country: "CN", ImdbID: "tt0106332"}
	got := applyMoviePatch(cur, updateMovieReq{Summary: sp("x")})
	if got.ID != "mv_1" || got.Title != "keep" || got.Country != "CN" || got.ImdbID != "tt0106332" {
		t.Fatalf("untouched fields changed: %+v", got)
	}
}
