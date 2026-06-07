package film

// auth.go — film-svc has no session store of its own; it asks dock to
// introspect Bearer tokens via /internal/v1/auth/verify (cached 30s in the
// SDK). Mirrors polar-dns/internal/dns/auth.go.

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ctxKeyUserID      = "user_id"
	ctxKeyUserRole    = "user_role"
	ctxKeyWorkspaceID = "workspace_id"
)

// requireAdminViaDock extracts Bearer → Dock.AuthVerify → role=admin.
func (p *Plugin) requireAdminViaDock() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAccessToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		res, err := p.Dock.AuthVerifyWS(token, strings.TrimSpace(c.GetHeader("X-Workspace-Id")))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}
		if !strings.EqualFold(res.Role, "admin") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "admin role required"})
			return
		}
		c.Set(ctxKeyUserID, res.UserID)
		c.Set(ctxKeyUserRole, res.Role)
		c.Set(ctxKeyWorkspaceID, p.resolveActiveWorkspace(c, res.WorkspaceID, res.UserID))
		c.Next()
	}
}

// requireAuthViaDock — Bearer + AuthVerify (any role) + closed-by-default
// workspace plugin-access gate.
func (p *Plugin) requireAuthViaDock() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAccessToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing bearer token"})
			return
		}
		res, err := p.Dock.AuthVerifyWS(token, strings.TrimSpace(c.GetHeader("X-Workspace-Id")))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return
		}
		wsID := p.resolveActiveWorkspace(c, res.WorkspaceID, res.UserID)
		c.Set(ctxKeyUserID, res.UserID)
		c.Set(ctxKeyUserRole, res.Role)
		c.Set(ctxKeyWorkspaceID, wsID)

		access, err := p.Dock.WorkspacePluginAccess(wsID, p.Name)
		if err != nil || access == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "plugin access check failed"})
			return
		}
		if !access.Enabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "workspace not granted access to film"})
			return
		}
		c.Next()
	}
}

func (p *Plugin) resolveActiveWorkspace(c *gin.Context, personalWS, userID string) string {
	return resolveWorkspaceID(c.GetHeader("X-Workspace-Id"), personalWS, userID, p.userIsTeamMember)
}

// resolveWorkspaceID — pure decision (no I/O), unit-testable: requested
// workspace wins only when non-empty, different from personal, and the user
// is a member; otherwise personal. Never 403s (no cross-tenant leak).
func resolveWorkspaceID(requested, personalWS, userID string, isMember func(teamID, userID string) bool) string {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == personalWS {
		return personalWS
	}
	if isMember != nil && isMember(requested, userID) {
		return requested
	}
	return personalWS
}

func (p *Plugin) userIsTeamMember(teamID, userID string) bool {
	teamID = strings.TrimSpace(teamID)
	userID = strings.TrimSpace(userID)
	if teamID == "" || userID == "" {
		return false
	}
	resp, err := p.Dock.Do(http.MethodGet,
		"/internal/v1/teams/"+url.PathEscape(teamID)+"/members/"+url.PathEscape(userID), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// extractAccessToken: Bearer header → ?access_token= → cookie.
func extractAccessToken(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("Authorization")); v != "" {
		if strings.HasPrefix(strings.ToLower(v), "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	if v := strings.TrimSpace(c.Query("access_token")); v != "" {
		return v
	}
	if v, err := c.Cookie("access_token"); err == nil && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return ""
}
