// Package argon2id provides a simple interface for hashing and verifying
// passwords using the Argon2id algorithm. It offers a default configuration
// that can be customized as needed.
package argon2id

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Predefined errors.
var (
	ErrInvalidHash         = errors.New("argon2id: invalid hash format")
	ErrIncompatibleVersion = errors.New("argon2id: incompatible version of argon2")
)

// Config holds the parameters for the Argon2id algorithm.
// Users can use DefaultConfig() for recommended defaults.
// !Important note: Changing the value of the Parallelism parameter changes the hash output.
type Config struct {
	Memory      uint32 // memory cost in kibibytes
	Iterations  uint32 // number of iterations
	Parallelism uint8  // degree of parallelism (number of threads)
	SaltLength  uint32 // length of the random salt in bytes
	KeyLength   uint32 // length of the generated key in bytes
}

// DefaultConfig returns a Config with recommended settings.
func DefaultConfig() *Config {
	return &Config{
		Memory:      64 * 1024, // 64 MB memory usage
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword generates a salted hash for the given password using the
// Argon2id algorithm and returns the encoded hash string.
func (c *Config) HashPassword(password string) (string, error) {
	salt, err := generateRandomBytes(c.SaltLength)
	if err != nil {
		return "", err
	}

	hash := argon2.IDKey([]byte(password), salt, c.Iterations, c.Memory, c.Parallelism, c.KeyLength)

	// Base64 encode the salt and hash without padding.
	b64Salt := base64.RawStdEncoding.EncodeToString(salt)
	b64Hash := base64.RawStdEncoding.EncodeToString(hash)

	// Format: $argon2id$v=VERSION$m=MEM,t=ITER,p=PARALLELISM$SALT$HASH
	encodedHash := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, c.Memory, c.Iterations, c.Parallelism, b64Salt, b64Hash)

	return encodedHash, nil
}

// ComparePasswordAndHash verifies whether the provided password matches the
// encoded hash. It returns true if they match.
func (c *Config) ComparePasswordAndHash(password, encodedHash string) (bool, error) {
	parsedConfig, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}

	// Derive key from the given password using parameters from the encoded hash.
	otherHash := argon2.IDKey([]byte(password), salt,
		parsedConfig.Iterations, parsedConfig.Memory,
		parsedConfig.Parallelism, parsedConfig.KeyLength)

	// Use constant time comparison to prevent timing attacks.
	if subtle.ConstantTimeCompare(hash, otherHash) == 1 {
		return true, nil
	}
	return false, nil
}

// generateRandomBytes returns securely generated random bytes.
func generateRandomBytes(n uint32) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// decodeHash parses an encoded Argon2id hash and returns the configuration,
// salt, and actual hash. It is intended for internal use.
func decodeHash(encodedHash string) (cfg *Config, salt, hash []byte, err error) {
	// Expected format: $argon2id$v=VERSION$m=MEM,t=ITER,p=PARALLELISM$SALT$HASH
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	_, err = fmt.Sscanf(parts[2], "v=%d", &version)
	if err != nil {
		return nil, nil, nil, err
	}
	if version != argon2.Version {
		return nil, nil, nil, ErrIncompatibleVersion
	}

	cfg = &Config{}
	_, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &cfg.Memory, &cfg.Iterations, &cfg.Parallelism)
	if err != nil {
		return nil, nil, nil, err
	}

	salt, err = base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.SaltLength = uint32(len(salt))

	hash, err = base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, err
	}
	cfg.KeyLength = uint32(len(hash))

	return cfg, salt, hash, nil
}
