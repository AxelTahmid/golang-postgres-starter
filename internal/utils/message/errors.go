package message

const (
	ErrBadRequest          = "error bad request"
	ErrInternalError       = "internal server error"
	ErrFormingResponse     = "error forming response"
	ErrUnauthorized        = "unauthorized access"
	ErrHostNotFound        = "host not found"
	ErrNoRecord            = "no record found"
	ErrPassOrUserIncorrect = "password or email is incorrect"
	ErrBadTokenFormat      = "authorization header format must be 'Bearer {token}'"
	ErrUserNotFound        = "user not found"
)
