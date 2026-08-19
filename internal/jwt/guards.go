package jwt

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/AxelTahmid/tinker/internal/httpx"
	"github.com/AxelTahmid/tinker/pkg/ctxkeys"
)

const (
	AuthHeaderKey = "Authorization"
	BearerPrefix  = "Bearer"

	// AuthCtxKey is where the bearer guards publish parsed claims. It is an
	// alias of the shared constant so the key has exactly one definition.
	AuthCtxKey = ctxkeys.AuthCtxKey
)

var errInvalidAuthHeader = errors.New("authorization header format must be 'Bearer {token}'")

type tokenParser func(string) (*Claims, error)

// AccessGuard validates a bearer access token and publishes its claims on the
// request context. The sealed guard carries the same security/error contract
// used to generate OpenAPI.
func AccessGuard() httpx.Guard {
	return bearerGuard("access-token", jwtSvc.ParseAccessToken)
}

// RefreshGuard validates a bearer refresh token. Refresh is POST-only so the
// credential is not replayed through query strings or URLs.
func RefreshGuard() httpx.Guard {
	return bearerGuard("refresh-token", jwtSvc.ParseRefreshToken)
}

func bearerGuard(id string, parse tokenParser) httpx.Guard {
	guard, err := httpx.NewGuard(httpx.GuardConfig{
		ID:          id,
		Credentials: httpx.Requires(httpx.SecuritySchemeBearerAuth),
		Problems:    []httpx.ProblemKind{httpx.Unauthorized()},
		Check: func(r *http.Request) (*http.Request, error) {
			header := r.Header.Get(AuthHeaderKey)
			if !strings.HasPrefix(header, BearerPrefix+" ") {
				return r, httpx.NewUnauthorizedError(errInvalidAuthHeader.Error())
			}
			claims, parseErr := parse(strings.TrimPrefix(header, BearerPrefix+" "))
			if parseErr != nil {
				return r, httpx.NewUnauthorizedError("invalid or expired token")
			}
			ctx := context.WithValue(r.Context(), AuthCtxKey, claims)
			return r.WithContext(ctx), nil
		},
	})
	if err != nil {
		panic(err)
	}
	return guard
}
