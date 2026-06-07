package film

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"

	"github.com/lib/pq"
)

// newID mints a prefixed, random TEXT id (e.g. mv_<32hex>), matching the
// platform convention of domain-prefixed string ids.
func newID(prefix string) string {
	b := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, b)
	return prefix + hex.EncodeToString(b)
}

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505) — used to translate duplicate inserts into HTTP 409.
func isUniqueViolation(err error) bool {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	return false
}

// existsInWorkspace reports whether a workspace-scoped row exists. `table`
// is an internal constant (e.g. "people", "tags"), never user input, so the
// concatenation is safe.
func (p *Plugin) existsInWorkspace(ctx context.Context, table, wsID, id string) bool {
	var one int
	err := p.DB.QueryRowContext(ctx, "SELECT 1 FROM "+table+" WHERE workspace_id=$1 AND id=$2", wsID, id).Scan(&one)
	return err == nil
}
