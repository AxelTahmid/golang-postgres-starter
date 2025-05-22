package jwt

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/AxelTahmid/tinker/internal/httpx"
)

type authCtxKey string

const (
	AuthHeaderKey            = "Authorization"
	BearerPrefix             = "Bearer"
	AuthCtxKey    authCtxKey = "ctx:auth-user"
)

var (
	errInvalidAuthHeader     = errors.New("authorization header format must be 'Bearer {token}'")
	errInsufficientPrivilege = errors.New("insufficient privileges")
)

type claimsValidator func(claims *Claims) error
type tokenParser func(string) (*Claims, error)

// AuthMiddleware - common middleware logic.
func AuthMiddleware(next http.Handler, parseToken tokenParser, validate claimsValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")

		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			httpx.Error(w, http.StatusUnauthorized, errInvalidAuthHeader.Error())
			return
		}

		// slog.Info("Header -===>", "authheader", authHeader, "bearer", !strings.HasPrefix(authHeader, "Bearer "))

		token := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := parseToken(token)
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, err.Error())
			return
		}

		if validate != nil {
			if err = validate(claims); err != nil {
				httpx.Error(w, http.StatusUnauthorized, errInsufficientPrivilege.Error())
				return
			}
		}

		r = r.WithContext(context.WithValue(r.Context(), AuthCtxKey, claims))
		next.ServeHTTP(w, r)
	})
}
func isAdmin(claims *Claims) error {
	if claims.Role != "admin" {
		return errInsufficientPrivilege
	}
	return nil
}

func isTenant(claims *Claims) error {
	if claims.Role != "tenant" {
		return errInsufficientPrivilege
	}
	return nil
}

func isTenantOrAdmin(claims *Claims) error {
	if claims.Role != "admin" && claims.Role != "tenant" {
		return errInsufficientPrivilege
	}
	return nil
}

// Middleware implementations.

func Authenticated(next http.Handler) http.Handler {
	return AuthMiddleware(next, jwtSvc.ParseAccessToken, nil)
}

func AdminOnly(next http.Handler) http.Handler {
	return AuthMiddleware(next, jwtSvc.ParseAccessToken, isAdmin)
}

func TenantOnly(next http.Handler) http.Handler {
	return AuthMiddleware(next, jwtSvc.ParseAccessToken, isTenant)
}

func TenantOrAdminOnly(next http.Handler) http.Handler {
	return AuthMiddleware(next, jwtSvc.ParseAccessToken, isTenantOrAdmin)
}

func RefreshFlow(next http.Handler) http.Handler {
	return AuthMiddleware(next, jwtSvc.ParseRefreshToken, nil)
}
