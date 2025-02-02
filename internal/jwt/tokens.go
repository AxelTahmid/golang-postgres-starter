package jwt

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// UserClaims represents user-specific claims for JWT.
type UserClaims struct {
	Email string
	Role  string
	Id    int64
}

// Tokens holds the generated access and refresh tokens.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// IssueTokenPair generates access and refresh tokens for a user.
func (j *JWTManager) IssueTokenPair(user *UserClaims) (*Tokens, error) {
	if user.Id == 0 || user.Email == "" || user.Role == "" {
		return nil, errors.New("invalid user data")
	}

	userID := strconv.FormatInt(user.Id, 10)

	accessClaims := CustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        userID,
			Issuer:    j.accessIssuer,
			Subject:   user.Email,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.accessTime)),
		},
		Role: user.Role,
	}

	accessToken, err := j.createToken(&accessClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to create access token: %w", err)
	}

	refreshClaims := CustomClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        userID,
			Issuer:    j.refreshIssuer,
			Subject:   user.Email,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.refreshTime)),
		},
		Role: user.Role,
	}

	refreshToken, err := j.createToken(&refreshClaims)
	if err != nil {
		return nil, fmt.Errorf("failed to create refresh token: %w", err)
	}

	return &Tokens{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
	}, nil
}

// createToken generates a signed JWT for the given claims.
func (j *JWTManager) createToken(claims *CustomClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	return token.SignedString(j.privateKey)
}
