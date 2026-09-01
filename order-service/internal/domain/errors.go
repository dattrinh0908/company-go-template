package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors describing failure *classes*. Transport layers map these to
// protocol-specific responses (see internal/transport/http/response.go) so that
// the domain never has to know about HTTP status codes.
var (
	// ErrNotFound means the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict means the request collides with existing state, such as a
	// duplicate key or a forbidden status transition.
	ErrConflict = errors.New("conflict")
	// ErrInvalidInput means the caller supplied data that fails validation.
	ErrInvalidInput = errors.New("invalid input")
)

// FieldError names a single field that failed validation.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationError aggregates every field problem found in one pass, so callers
// can fix an entire payload at once instead of one field per round trip.
type ValidationError struct {
	Fields []FieldError
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, f := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f.Field, f.Message))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Unwrap lets errors.Is(err, ErrInvalidInput) succeed for validation failures.
func (e *ValidationError) Unwrap() error { return ErrInvalidInput }

// Add records a field problem.
func (e *ValidationError) Add(field, message string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: message})
}

// OrNil returns nil when nothing failed, which keeps callers to a single
// `if err := v.OrNil(); err != nil` line.
func (e *ValidationError) OrNil() error {
	if len(e.Fields) == 0 {
		return nil
	}
	return e
}
