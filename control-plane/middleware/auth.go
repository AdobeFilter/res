package middleware

import (
	"context"
	"net/http"
	"strings"

	"valhalla/common/crypto"
)

type contextKey string

const (
	ContextAccountID contextKey = "account_id"
	ContextNodeID    contextKey = "node_id"
)

// Auth returns middleware that validates JWT tokens.
func Auth(tm *crypto.TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, `{"code":401,"message":"missing authorization header"}`, http.StatusUnauthorized)
				return
			}

			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || strings.ToLower(parts[0]) != "bearer" {
				http.Error(w, `{"code":401,"message":"invalid authorization format"}`, http.StatusUnauthorized)
				return
			}

			claims, err := tm.ValidateToken(parts[1])
			if err != nil {
				http.Error(w, `{"code":401,"message":"invalid or expired token"}`, http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ContextAccountID, claims.AccountID)
			if claims.NodeID != "" {
				ctx = context.WithValue(ctx, ContextNodeID, claims.NodeID)
			}

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetAccountID extracts account ID from request context.
func GetAccountID(ctx context.Context) string {
	if v, ok := ctx.Value(ContextAccountID).(string); ok {
		return v
	}
	return ""
}

// GetNodeID extracts node ID from request context.
func GetNodeID(ctx context.Context) string {
	if v, ok := ctx.Value(ContextNodeID).(string); ok {
		return v
	}
	return ""
}
