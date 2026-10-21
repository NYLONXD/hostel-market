package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
)

var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report field names as the client sent them (json tags), not Go names.
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			return ""
		}
		return name
	})
	return v
}

// Validator exposes the shared instance so modules can register custom rules at startup.
func Validator() *validator.Validate { return validate }

// FieldError describes one failed rule on one field.
type FieldError struct {
	Field   string `json:"field"`
	Rule    string `json:"rule"`
	Param   string `json:"param,omitempty"`
	Message string `json:"message"`
}

// Validate runs struct-tag validation and converts failures into a 422 with field details.
func Validate(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		return Internal(fmt.Errorf("validate: %w", err)) // programmer error, e.g. non-struct
	}
	fields := make([]FieldError, 0, len(verrs))
	for _, fe := range verrs {
		fields = append(fields, FieldError{
			Field:   fieldPath(fe.Namespace()),
			Rule:    fe.Tag(),
			Param:   fe.Param(),
			Message: fieldMessage(fe),
		})
	}
	return ValidationFailed(fields...)
}

// ValidationFailed builds a 422 from field errors; services use it for rules that
// struct tags can't express.
func ValidationFailed(fields ...FieldError) *Error {
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    "validation_failed",
		Message: "request validation failed",
		Details: fields,
	}
}

// fieldPath drops the top-level struct name: "CreateReq.photos[0].public_id" -> "photos[0].public_id".
func fieldPath(ns string) string {
	if _, rest, ok := strings.Cut(ns, "."); ok {
		return rest
	}
	return ns
}

func fieldMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required", "required_if", "required_unless", "required_with", "required_without":
		return "is required"
	case "excluded_if", "excluded_unless", "excluded_with", "excluded_without":
		return "must not be set here"
	case "email":
		return "must be a valid email address"
	case "uuid", "uuid4":
		return "must be a UUID"
	case "oneof":
		return "must be one of: " + fe.Param()
	case "min":
		if fe.Kind() == reflect.String || fe.Kind() == reflect.Slice {
			return "must have at least " + fe.Param() + " characters/items"
		}
		return "must be at least " + fe.Param()
	case "max":
		if fe.Kind() == reflect.String || fe.Kind() == reflect.Slice {
			return "must have at most " + fe.Param() + " characters/items"
		}
		return "must be at most " + fe.Param()
	case "len":
		return "must have length " + fe.Param()
	case "gt":
		return "must be greater than " + fe.Param()
	case "gte":
		return "must be at least " + fe.Param()
	case "lt":
		return "must be less than " + fe.Param()
	case "lte":
		return "must be at most " + fe.Param()
	default:
		return "failed the '" + fe.Tag() + "' rule"
	}
}
