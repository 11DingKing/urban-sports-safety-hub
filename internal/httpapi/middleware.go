package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/11DingKing/urban-sports-safety-hub/internal/auth"
	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type Middleware struct {
	auth   *auth.Service
	logger *slog.Logger
}

func (m Middleware) Observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		id := requestID(r)
		ctx := withRequestID(r.Context(), id)
		w.Header().Set("X-Request-ID", id)
		defer func() {
			if recovered := recover(); recovered != nil {
				m.logger.Error("http panic recovered", "request_id", id, "panic", recovered, "stack", string(debug.Stack()))
				writeError(w, r.WithContext(ctx), domain.NewError(domain.KindUnavailable, "internal_error", "the service could not complete the request"))
			}
			m.logger.Info("http request", "request_id", id, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeError(w, r, domain.NewError(domain.KindUnauthorized, "missing_session", "a bearer session token is required"))
			return
		}
		principal, err := m.auth.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(withPrincipal(r.Context(), principal)))
	})
}
