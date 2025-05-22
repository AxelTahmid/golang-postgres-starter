package auth

import "encoding/json"

type RegisterRequest struct {
	Email    string `json:"email"      validate:"required,email,lowercase"`
	Password string `json:"password"   validate:"required,min=6"`
	Role     string `json:"role"       validate:"required,oneof=admin customer"`
	// Customer-specific fields
	FirstName string `json:"first_name" validate:"required_if=Role customer"`
	LastName  string `json:"last_name"  validate:"required_if=Role customer"`
}

type LoginRequest struct {
	Identity string `json:"identity" validate:"required,email_or_e164,lowercase"`
	Password string `json:"password" validate:"required,min=6"`
}

type MeResponse struct {
	AuthUser json.RawMessage `json:"auth_user"`
}
