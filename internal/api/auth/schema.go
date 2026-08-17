package auth

import (
	"time"

	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

type RegisterRequest struct {
	Email    string `json:"email"    validate:"required,email,lowercase"`
	Password string `json:"password" validate:"required,min=8"`
}

type LoginRequest struct {
	Identity string `json:"identity" validate:"required,email_or_e164,lowercase"`
	Password string `json:"password" validate:"required,min=8"`
}

type RegisterInput struct {
	Body RegisterRequest `body:"required"`
}

type LoginInput struct {
	Body LoginRequest `body:"required"`
}

type EmptyInput struct{}

// UserResponse is the client-safe projection of an auth user. Password hashes
// and other authentication internals must never be serialized.
type UserResponse struct {
	ID            int64         `json:"id"`
	Email         string        `json:"email"`
	EmailVerified bool          `json:"email_verified"`
	Phone         string        `json:"phone"`
	PhoneVerified bool          `json:"phone_verified"`
	Role          sqlc.RoleType `json:"role"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

func userResponse(user sqlc.AuthUser) UserResponse {
	return UserResponse{
		ID:            user.ID,
		Email:         user.Email,
		EmailVerified: user.EmailVerified,
		Phone:         user.Phone,
		PhoneVerified: user.PhoneVerified,
		Role:          user.Role,
		CreatedAt:     user.CreatedAt.Time,
		UpdatedAt:     user.UpdatedAt.Time,
	}
}
