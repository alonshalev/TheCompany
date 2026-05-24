// Package auth — HTTP middleware for API key authentication.
package auth

import (
	"context"
	"database/sql"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/symbiont-ai/symbiont/internal/models"
)

type contextKey string

const (
	contextKeyUser    contextKey = "user"
	contextKeyOrgID   contextKey = "org_id"
)

// UserLookupFn is a function that looks up a user by their API key hash.
// Implementations query the database.
type UserLookupFn func(ctx context.Context, keyHash string) (*models.User, error)

// RequireAPIKey returns middleware that validates the Authorization header.
// It expects: Authorization: Bearer sym_<key>
func RequireAPIKey(lookup UserLookupFn) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, ok := extractBearerToken(r)
			if !ok {
				writeUnauthorized(w, "missing or malformed Authorization header")
				return
			}

			if !IsValidAPIKeyFormat(key) {
				writeUnauthorized(w, "invalid API key format")
				return
			}

			hash := HashAPIKey(key)
			user, err := lookup(r.Context(), hash)
			if err != nil {
				if err == sql.ErrNoRows {
					writeUnauthorized(w, "invalid API key")
					return
				}
				log.Error().Err(err).Msg("auth: user lookup failed")
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			// Inject user into context
			ctx := context.WithValue(r.Context(), contextKeyUser, user)
			ctx = context.WithValue(ctx, contextKeyOrgID, user.OrgID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext extracts the authenticated user from the context.
func UserFromContext(ctx context.Context) (*models.User, bool) {
	u, ok := ctx.Value(contextKeyUser).(*models.User)
	return u, ok
}

// Actor is a convenience bundle used by audit helpers.
type Actor struct {
	UserID    uuid.UUID
	OrgID     uuid.UUID
	ProjectID *uuid.UUID // nil when the action is not project-scoped
	Type      string     // "user" | "system"
	Name      *string
}

// ActorFromContext builds an Actor from the authenticated user in ctx.
// Falls back to a system actor if no user is present.
func ActorFromContext(ctx context.Context) Actor {
	user, ok := UserFromContext(ctx)
	if !ok {
		return Actor{Type: "system"}
	}
	return Actor{
		UserID: user.ID,
		OrgID:  user.OrgID,
		Type:   "user",
		Name:   &user.Name,
	}
}

// RequireRole returns middleware that checks the user has at least the given role.
// Must be used after RequireAPIKey.
func RequireRole(minRole models.UserRole) func(http.Handler) http.Handler {
	roleRank := map[models.UserRole]int{
		models.UserRoleViewer: 0,
		models.UserRoleMember: 1,
		models.UserRoleAdmin:  2,
		models.UserRoleOwner:  3,
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := UserFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, "not authenticated")
				return
			}
			if roleRank[user.Role] < roleRank[minRole] {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

func writeUnauthorized(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"error":"` + msg + `"}`))
}
