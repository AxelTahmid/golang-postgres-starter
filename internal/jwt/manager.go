package jwt

import (
	"crypto/ecdsa"
	"fmt"
	"os"
	"time"

	"github.com/AxelTahmid/tinker/config"
	"github.com/golang-jwt/jwt/v5"
)

// Singleton instance of JWTManager.
var Service *JWTManager

// JWTManager manages token creation and parsing.
type JWTManager struct {
	accessTime    time.Duration
	refreshTime   time.Duration
	privateKey    *ecdsa.PrivateKey
	publicKey     *ecdsa.PublicKey
	accessIssuer  string
	refreshIssuer string
}

// NewJWTManager initializes a new JWTManager.
func NewJWT(c *config.Jwt) error {
	if Service != nil {
		return nil // Singleton already initialized.
	}

	privateKey, publicKey, err := loadKeys(c.JwtPubKeyPath, c.JwtPvtKeyPath)
	if err != nil {
		return err
	}

	Service = &JWTManager{
		privateKey:    privateKey,
		publicKey:     publicKey,
		accessTime:    c.AccessExpiryTime,
		refreshTime:   c.RefreshExpiryTime,
		accessIssuer:  c.AccessTokenIssuer,
		refreshIssuer: c.RefreshTokenIssuer,
	}

	return nil
}

// loadKeys loads private and public keys from file paths.
func loadKeys(publicKeyPath, privateKeyPath string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read private key: %w", err)
	}

	privateKey, err := jwt.ParseECPrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	publicKeyPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read public key: %w", err)
	}

	publicKey, err := jwt.ParseECPublicKeyFromPEM(publicKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	return privateKey, publicKey, nil
}
