package validate

import (
	"strings"

	"github.com/go-playground/validator/v10"
)

type Validate struct {
	validator *validator.Validate
	messages  map[string]string
}

func New() *Validate {
	v := validator.New()
	// v.RegisterTagNameFunc(jsonFieldName)

	return &Validate{
		validator: v,
		messages:  defaultMessages(),
	}
}

// func jsonFieldName(fld reflect.StructField) string {
// 	name := strings.SplitN(fld.Tag.Get("json"), ",", 2)[0]
// 	if name == "-" || name == "" {
// 		return fld.Name // Fallback to struct field name
// 	}
// 	return name
// }

func defaultMessages() map[string]string {
	return map[string]string{
		"required": "{0} is required",
		"email":    "{0} must be a valid email address",
		"min":      "{0} must be at least {1} characters",
		"max":      "{0} must be at most {1} characters",
		"numeric":  "{0} must be a valid number",
	}
}

// AddCustomMessages merges custom messages with existing ones
func (v *Validate) AddCustomMessages(messages map[string]string) {
	for key, msg := range messages {
		v.messages[key] = msg
	}
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (v *Validate) Check(data interface{}) map[string]string {
	err := v.validator.Struct(data)
	if err == nil {
		return nil
	}

	errors := make(map[string]string)

	for _, verr := range err.(validator.ValidationErrors) {
		field := verr.StructField()
		tag := verr.Tag()
		param := verr.Param()

		msgTemplate := v.getMessage(tag)
		msg := strings.NewReplacer(
			"{0}", field,
			"{1}", param,
		).Replace(msgTemplate)

		if existing, exists := errors[field]; exists {
			errors[field] = existing + "; " + msg
		} else {
			errors[field] = msg
		}
	}

	return errors
}

func (v *Validate) getMessage(tag string) string {
	if msg, exists := v.messages[tag]; exists {
		return msg
	}
	return "{0} failed validation"
}
