package auth

type RegisterRequest struct {
	Email    string `json:"email" validate:"required,email,lowercase"`
	Password string `json:"password" validate:"required,min=6"`
	Role     string `json:"role" validate:"required,oneof=admin tenant customer"`
	Phone    string `json:"phone,omitempty" validate:"required_if=Role tenant,omitempty,e164"`
	// Tenant-specific fields
	FullName    string `json:"full_name,omitempty" validate:"required_if=Role tenant,omitempty"`
	CompanyName string `json:"company_name,omitempty" validate:"required_if=Role tenant,omitempty"`
	Domain      string `json:"domain,omitempty" validate:"required_if=Role tenant,omitempty"`
	// Customer-specific fields
	FirstName string `json:"first_name,omitempty" validate:"required_if=Role customer,omitempty"`
	LastName  string `json:"last_name,omitempty" validate:"required_if=Role customer,omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required,min=6"`
}
