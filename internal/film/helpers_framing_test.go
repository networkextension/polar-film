package film

// helpers_framing_test.go — regression guard for jsonLen: large JSON responses
// must carry an explicit Content-Length and stay identity-encoded (NOT
// Transfer-Encoding: chunked), which some proxies/clients choke on.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestJSONLen_SetsContentLength_NoChunked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	// A body comfortably larger than net/http's ~2KB auto-buffer, which is the
	// threshold past which plain c.JSON falls back to chunked.
	big := make([]gin.H, 200)
	for i := range big {
		big[i] = gin.H{"id": "sc_" + strconv.Itoa(i), "phash": strings.Repeat("a", 16)}
	}
	r.GET("/x", func(c *gin.Context) { jsonLen(c, http.StatusOK, gin.H{"screenshots": big}) })

	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if len(resp.TransferEncoding) != 0 {
		t.Fatalf("expected identity encoding, got Transfer-Encoding: %v", resp.TransferEncoding)
	}
	if resp.ContentLength < 0 {
		t.Fatalf("expected explicit Content-Length, got %d (chunked/unknown)", resp.ContentLength)
	}
	if int(resp.ContentLength) != len(body) {
		t.Fatalf("Content-Length %d != body bytes %d", resp.ContentLength, len(body))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}
}
