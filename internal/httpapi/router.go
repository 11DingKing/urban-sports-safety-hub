package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
	"github.com/11DingKing/urban-sports-safety-hub/internal/enrollment"
	"github.com/11DingKing/urban-sports-safety-hub/internal/equipment"
)

type HealthStore interface {
	Ping(ctx context.Context) error
}

type API struct {
	auth       *auth.Service
	enrollment *enrollment.Service
	equipment  *equipment.Service
	health     HealthStore
	logger     *slog.Logger
}

func New(authService *auth.Service, enrollmentService *enrollment.Service, equipmentService *equipment.Service, health HealthStore, logger *slog.Logger) *API {
	return &API{auth: authService, enrollment: enrollmentService, equipment: equipmentService, health: health, logger: logger}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()
	middleware := Middleware{auth: a.auth, logger: a.logger}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
	})
	mux.HandleFunc("GET /readyz", a.ready)
	mux.HandleFunc("POST /api/login", a.login)
	mux.Handle("POST /api/logout", middleware.Authenticate(http.HandlerFunc(a.logout)))
	mux.Handle("POST /api/enrollments", middleware.Authenticate(http.HandlerFunc(a.enroll)))
	mux.Handle("POST /api/course-sessions/{id}/cancel", middleware.Authenticate(http.HandlerFunc(a.cancelCourse)))
	mux.Handle("POST /api/equipment/checkout", middleware.Authenticate(http.HandlerFunc(a.checkout)))
	mux.Handle("POST /api/equipment/return", middleware.Authenticate(http.HandlerFunc(a.returnEquipment)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, domain.NewError(domain.KindNotFound, "route_not_found", "route was not found"))
	})
	return middleware.Observe(mux)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	if err := a.health.Ping(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
