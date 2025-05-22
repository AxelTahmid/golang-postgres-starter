package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgtype"
)

// Validator instance.
var validate *validator.Validate

// ValidationErrorDetail provides structured details about a single validation error.
type ValidationErrorDetail struct {
	Field   string `json:"field"`   // The name of the field that failed validation.
	Message string `json:"message"` // A human-readable message describing the error.
}

func NewValidator() error {
	validate = validator.New(validator.WithRequiredStructEnabled())

	if err := validate.RegisterValidation("email_or_e164", EmailOrE164); err != nil {
		return fmt.Errorf("Error registering email_or_e164 validation: %w", err)
	}

	if err := validate.RegisterValidation("pg_numeric_gt", PgNumericGt); err != nil {
		return fmt.Errorf("Error registering pg_numeric_gt validation: %w", err)
	}

	return nil
}

// NewValidationProblemDetails creates a ProblemDetails instance based on validation errors.
// It maps field-specific validation errors into structured details.
func NewValidationProblemDetails(err error) *ProblemDetails {
	var validationErrors validator.ValidationErrors
	if !errors.As(err, &validationErrors) {
		// If the error is not of type ValidationErrors, return a generic problem response.
		return NewProblemDetails(
			http.StatusBadRequest,
			GetProblemTypeURL("bad_request_error"),
			"Invalid Request",
			"Invalid data format or structure",
		)
	}

	// Collect structured details about each validation error.
	errorDetails := make([]ValidationErrorDetail, len(validationErrors))
	for i, vErr := range validationErrors {
		errorDetails[i] = ValidationErrorDetail{
			Field:   vErr.Field(),
			Message: formatValidationMessage(vErr),
		}
	}

	return &ProblemDetails{
		Type:   GetProblemTypeURL("validation_error"),
		Title:  "Validation Error",
		Status: http.StatusBadRequest,
		Detail: "One or more fields failed validation.",
		Extensions: map[string]interface{}{
			"errors": errorDetails,
		},
	}
}

// formatValidationMessage generates a descriptive message for a validation error.
func formatValidationMessage(vErr validator.FieldError) string {
	return vErr.Field() + " failed " + vErr.Tag() + " validation"
}

// IsRequestValid validates the provided request struct using the go-playground/validator package.
// It returns a ProblemDetails instance if validation fails, or nil if the request is valid.
func IsRequestValid(request any) *ProblemDetails {
	err := validate.Struct(request)
	if err != nil {
		return NewValidationProblemDetails(err)
	}
	return nil
}

// EmailOrE164 is a custom validator function that returns true if the field value
// is either a valid email or a valid E.164 phone number.
func EmailOrE164(fl validator.FieldLevel) bool {
	value := fl.Field().String()
	if value == "" {
		// Let the "required" tag handle emptiness.
		return true
	}

	// Check if the value passes the email validation.
	if err := validate.Var(value, "email"); err == nil {
		return true
	}

	// Check if the value passes the e164 validation.
	if err := validate.Var(value, "e164"); err == nil {
		return true
	}

	return false
}

func PgNumericGt(fl validator.FieldLevel) bool {
	numericField, ok := fl.Field().Interface().(pgtype.Numeric)
	if !ok {
		return false
	}

	if !numericField.Valid {
		return false
	}

	floatVal, err := numericField.Float64Value()
	if err != nil {
		return false
	}

	param := fl.Param()
	if param == "" {
		return false
	}

	threshold, err := strconv.ParseFloat(param, 64)
	if err != nil {
		return false
	}

	// Return validation result
	return floatVal.Float64 > threshold
}
