package jwt

import (
	"crypto/ecdsa"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/AxelTahmid/tinker/config"
)

const (
	AccessIssuer  string = "auth-access"
	RefreshIssuer string = "auth-refresh"
)

type Service interface {
	ParseRefreshToken(token string) (*Claims, error)
	ParseAccessToken(token string) (*Claims, error)
	IssueTokenPair(
		userID int64,
		email, role string,
	) (*Tokens, error)
}

// jwtService manages token creation and parsing.
type jwtService struct {
	privateKey  *ecdsa.PrivateKey
	publicKey   *ecdsa.PublicKey
	accessTime  time.Duration
	refreshTime time.Duration
	clockSkew   time.Duration
}

// Singleton instance of jwtService.
var (
	jwtOnce sync.Once
	jwtSvc  *jwtService
)

// InitJWT initializes a new jwtService.
func InitJWT(c *config.Jwt) error {
	var initErr error
	slog.Debug("Initializing JWT service")
	jwtOnce.Do(func() {
		privateKey, publicKey, err := loadKeys(c.PubKeyPath, c.PvtKeyPath)
		if err != nil {
			initErr = err
			return
		}

		// Set reasonable defaults for clock skew if not provided
		clockSkew := c.ClockSkew
		if clockSkew == 0 {
			clockSkew = 10 * time.Second
		}

		jwtSvc = &jwtService{
			privateKey:  privateKey,
			publicKey:   publicKey,
			accessTime:  c.AccessExpiryTime,
			refreshTime: c.RefreshExpiryTime,
			clockSkew:   clockSkew,
		}
	})

	if initErr != nil {
		return fmt.Errorf("failed to initialize jwt service: %w", initErr)
	}
	slog.Debug("JWT service initialized")
	return nil
}

// loadKeys loads private and public ECDSA keys from files
func loadKeys(publicKeyPath, privateKeyPath string) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read private key from %s: %w", privateKeyPath, err)
	}

	privateKey, err := jwt.ParseECPrivateKeyFromPEM(privateKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse ECDSA private key: %w", err)
	}

	publicKeyPEM, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read public key from %s: %w", publicKeyPath, err)
	}

	publicKey, err := jwt.ParseECPublicKeyFromPEM(publicKeyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse ECDSA public key: %w", err)
	}

	return privateKey, publicKey, nil
}

func GetService() Service {
	return jwtSvc
}
