package jwt

import (
	"context"
	"errors"
	"log/slog"

	"github.com/AxelTahmid/tinker/internal/utils/ctxkeys"
	"github.com/golang-jwt/jwt/v5"
)

type CustomClaims struct {
	jwt.RegisteredClaims
	Role string `json:"role,omitempty"`
}

// parseTokenClaims parses a token and validates its claims.
func (j *JWTManager) parseTokenClaims(token, expectedIssuer string) (*CustomClaims, error) {
	parsedToken, err := jwt.ParseWithClaims(token, &CustomClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			slog.Error("unexpected signing method")
			return nil, errors.New("invalid token")
		}
		return j.publicKey, nil
	})
	if err != nil {
		slog.Error("token parsing failed: " + err.Error())
		return nil, errors.New("invalid token")
	}

	claims, ok := parsedToken.Claims.(*CustomClaims)
	if !ok || !parsedToken.Valid {
		return nil, errors.New("invalid token")
	}

	if claims.Issuer != expectedIssuer {
		slog.Error("issuer mismatch: expected: " + expectedIssuer + ", got: " + claims.Issuer)
		return nil, errors.New("invalid token")
	}

	return claims, nil
}

// Public methods for parsing tokens
func (j *JWTManager) ParseAccessToken(token string) (*CustomClaims, error) {
	return j.parseTokenClaims(token, j.accessIssuer)
}

func (j *JWTManager) ParseRefreshToken(token string) (*CustomClaims, error) {
	return j.parseTokenClaims(token, j.refreshIssuer)
}

// Helpers for context interaction
func AddClaimsToContext(ctx context.Context, claims *CustomClaims) context.Context {
	return context.WithValue(ctx, ctxkeys.AuthUser, claims)
}

func GetClaimsFromContext(ctx context.Context) (*CustomClaims, error) {
	claims, ok := ctx.Value(ctxkeys.AuthUser).(*CustomClaims)
	if !ok {
		return nil, errors.New("failed to parse claims from context")
	}
	return claims, nil
}
