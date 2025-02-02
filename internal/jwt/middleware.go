package jwt

import (
	"errors"
	"net/http"
	"strings"

	"github.com/AxelTahmid/tinker/internal/utils/message"
	"github.com/AxelTahmid/tinker/internal/utils/respond"
)

const (
	authHeaderKey = "Authorization"
	bearerPrefix  = "Bearer"
)

type claimsValidator func(claims *CustomClaims) error
type tokenParser func(string) (*CustomClaims, error)

// Parse and validate Authorization header
func parseAuthorizationHeader(r *http.Request) (string, error) {
	authHeader := r.Header.Get(authHeaderKey)
	if authHeader == "" {
		return "", errors.New(message.ErrUnauthorized)
	}

	parts := strings.Split(authHeader, " ")
	if len(parts) != 2 || parts[0] != bearerPrefix {
		return "", errors.New(message.ErrBadTokenFormat)
	}

	return parts[1], nil
}

// Common middleware logic
func withAuth(next http.Handler, parseToken tokenParser, validate claimsValidator) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		token, err := parseAuthorizationHeader(r)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, err.Error())
			return
		}

		claims, err := parseToken(token)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, err.Error())
			return
		}

		if validate != nil {
			if err := validate(claims); err != nil {
				respond.Error(w, http.StatusUnauthorized, err.Error())
				return
			}
		}

		r = r.WithContext(AddClaimsToContext(r.Context(), claims))
		next.ServeHTTP(w, r)
	})
}

// Middleware implementations
func Authenticated(next http.Handler) http.Handler {
	return withAuth(next, Service.ParseAccessToken, nil)
}

func AdminOnly(next http.Handler) http.Handler {
	return withAuth(next, Service.ParseAccessToken, func(claims *CustomClaims) error {
		if claims.Role != "admin" {
			return errors.New(message.ErrUnauthorized)
		}
		return nil
	})
}

func TenantOrAdminOnly(next http.Handler) http.Handler {
	return withAuth(next, Service.ParseAccessToken, func(claims *CustomClaims) error {
		if claims.Role != "admin" && claims.Role != "tenant" {
			return errors.New(message.ErrUnauthorized)
		}
		return nil
	})
}

func RefreshFlow(next http.Handler) http.Handler {
	return withAuth(next, Service.ParseRefreshToken, nil)
}
