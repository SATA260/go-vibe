package routes

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type healthResponse struct {
	OK bool `json:"ok"`
}

// RegisterHealth 注册健康检查接口，用于确认 Go server 已启动并能正常返回 JSON。
func RegisterHealth(r chi.Router) {
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(healthResponse{OK: true})
	})
}
