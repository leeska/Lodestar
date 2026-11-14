package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/leeska/Lodestar/internal/model"
)

// mcpKeyRoutes keep the settings page's single "MCP key": it is the access key
// with id "mcp" and full rights. Keys with their own name and limits are made
// on the Agent 接入 page (keyRoutes).
func (a *API) mcpKeyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		out := map[string]any{"configured": false}
		if a.Keys != nil {
			key, err := a.Keys.Get(r.Context(), "mcp")
			if err != nil && !errors.Is(err, model.ErrNotFound) {
				fail(w, err)
				return
			}
			if err == nil && key.Active(time.Now()) {
				out["configured"], out["created_at"] = true, time.Unix(key.CreatedAt, 0).UTC()
			}
		}
		write(w, 200, out)
	}))
	mux.HandleFunc("POST /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if a.Keys == nil {
			write(w, 503, map[string]string{"error": "访问密钥不可用"})
			return
		}
		key := "lodestar_mcp_" + model.Secret()
		if err := a.Keys.SetLegacyMCP(r.Context(), key); err != nil {
			fail(w, err)
			return
		}
		write(w, 200, map[string]any{"configured": true, "created_at": time.Now().UTC(), "api_key": key})
	}))
	mux.HandleFunc("DELETE /api/v1/admin/mcp-key", a.admin(func(w http.ResponseWriter, r *http.Request) {
		if a.Keys == nil {
			respond(w, nil)
			return
		}
		respond(w, a.Keys.RevokeLegacyMCP(r.Context()))
	}))
}
