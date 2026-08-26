package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/11DingKing/urban-sports-safety-hub/internal/domain"
)

type contextKey string

const (
	principalKey contextKey = "principal"
	requestIDKey contextKey = "request_id"
)

func withPrincipal(ctx context.Context, principal domain.Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

func principalFrom(ctx context.Context) (domain.Principal, bool) {
	principal, ok := ctx.Value(principalKey).(domain.Principal)
	return principal, ok
}

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

func requestID(r *http.Request) string {
	if supplied := strings.TrimSpace(r.Header.Get("X-Request-ID")); supplied != "" && len(supplied) <= 128 {
		return supplied
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "request-unavailable"
	}
	return hex.EncodeToString(raw)
}
