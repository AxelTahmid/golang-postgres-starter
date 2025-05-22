package jwt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	errInvalidToken  = errors.New("invalid token")
	errInvalidClaims = errors.New("invalid token claims")
	errTokenNotFound = errors.New("token not found in context")
)

// Claims represents all JWT claims used in the system.
type Claims struct {
	*jwt.RegisteredClaims
	// Application-specific claims
	Role string `json:"role,omitempty"`
}

// Tokens holds the generated access and refresh tokens.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// type TokenType int

// const (
// 	AccessToken TokenType = iota
// 	RefreshToken
// )

// createToken generates a signed JWT for the given claims.
func (j *jwtService) createToken(claims *Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	return token.SignedString(j.privateKey)
}

// IssueTokenPair generates access and refresh tokens for a user.
func (j *jwtService) IssueTokenPair(
	userID int64,
	email, role string,
) (*Tokens, error) {
	if userID == 0 {
		return nil, fmt.Errorf("%w: user ID cannot be zero", errInvalidClaims)
	}
	if email == "" {
		return nil, fmt.Errorf("%w: email cannot be empty", errInvalidClaims)
	}
	if role == "" {
		return nil, fmt.Errorf("%w: role cannot be empty", errInvalidClaims)
	}

	userIDStr := strconv.FormatInt(userID, 10)
	now := time.Now()

	accessClaims := &Claims{
		RegisteredClaims: &jwt.RegisteredClaims{
			ID:        userIDStr,
			Issuer:    AccessIssuer,
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.accessTime)),
		},
		Role: role,
	}

	accessToken, err := j.createToken(accessClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to create access token: %w", err)
	}

	refreshClaims := &Claims{
		RegisteredClaims: &jwt.RegisteredClaims{
			ID:        userIDStr,
			Issuer:    RefreshIssuer,
			Subject:   email,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(j.refreshTime)),
		},
	}

	refreshToken, err := j.createToken(refreshClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh token: %w", err)
	}

	return &Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(j.accessTime.Seconds()),
	}, nil
}

// parseTokenClaims parses a token and validates its claims.
func (j *jwtService) parseTokenClaims(token, expectedIssuer string) (*Claims, error) {
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodES256.Name}),
		jwt.WithLeeway(j.clockSkew),
		jwt.WithIssuer(expectedIssuer))

	// Parse the token
	claims := &Claims{}
	parsedToken, err := parser.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return j.publicKey, nil
	})

	if err != nil {
		slog.Error("token parsing failed: ", "error", err)
		return nil, fmt.Errorf("%w: %w", errInvalidToken, err)
	}

	if !parsedToken.Valid {
		return nil, errInvalidToken
	}

	// issuer is already checked by WithIssuer
	return claims, nil
}

// Public methods for parsing tokens.
func (j *jwtService) ParseAccessToken(token string) (*Claims, error) {
	return j.parseTokenClaims(token, AccessIssuer)
}

func (j *jwtService) ParseRefreshToken(token string) (*Claims, error) {
	return j.parseTokenClaims(token, RefreshIssuer)
}

func ParseClaimsCtx(ctx context.Context) (*Claims, int64, error) {
	raw, ok := ctx.Value(AuthCtxKey).(*Claims)
	if !ok {
		return nil, 0, errTokenNotFound
	}

	// parse the RegisteredClaims.ID (string) into an int64
	uid, err := strconv.ParseInt(raw.RegisteredClaims.ID, 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid user ID in token claims: %w", err)
	}

	return raw, uid, nil
}
