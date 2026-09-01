// Package http adapts the domain to the outside world over HTTP.
//
// Handlers here are deliberately thin: decode, delegate to a domain service,
// encode. Business rules belong in internal/domain, SQL belongs in
// internal/repository. If a handler starts growing conditionals about what the
// business means, that logic is in the wrong layer.
package http

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"order-service/internal/domain"
	"order-service/internal/observability"
)

// ErrorBody is the single error shape every endpoint returns, so clients can
// write one error handler instead of one per route.
type ErrorBody struct {
	Code      string              `json:"code"`
	Message   string              `json:"message"`
	Fields    []domain.FieldError `json:"fields,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
}

// errorResponse is the JSON envelope for a failure.
type errorResponse struct {
	Error ErrorBody `json:"error"`
}

// respond writes a success payload.
func respond(c *gin.Context, status int, payload any) {
	c.JSON(status, payload)
}

// respondError maps a domain error onto an HTTP status and a stable error code.
//
// This function is the only place in the service that knows both vocabularies.
// Adding a new failure class means adding one case here, not touching handlers.
func respondError(c *gin.Context, err error) {
	ctx := c.Request.Context()
	logger := observability.LoggerFrom(ctx)

	status, code := http.StatusInternalServerError, "internal_error"
	message := "an unexpected error occurred"
	var fields []domain.FieldError

	var validation *domain.ValidationError
	switch {
	case errors.As(err, &validation):
		status, code = http.StatusUnprocessableEntity, "validation_failed"
		message = "the request payload failed validation"
		fields = validation.Fields

	case errors.Is(err, domain.ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
		message = "the requested resource does not exist"

	case errors.Is(err, domain.ErrConflict):
		status, code = http.StatusConflict, "conflict"
		// A conflict message describes a business rule, not an internal
		// detail, so it is safe and useful to surface verbatim.
		message = err.Error()

	case errors.Is(err, domain.ErrInvalidInput):
		status, code = http.StatusBadRequest, "invalid_input"
		message = err.Error()

	case errors.Is(err, context.Canceled):
		// The client hung up. 499 is nginx's non-standard code for it; it is
		// logged as a warning rather than an error because nothing broke.
		status, code = 499, "client_closed_request"
		message = "the client closed the request"

	case errors.Is(err, context.DeadlineExceeded):
		status, code = http.StatusGatewayTimeout, "timeout"
		message = "the request took too long to process"
	}

	if status >= http.StatusInternalServerError {
		// Internal failures are logged in full but never echoed to the caller,
		// which keeps stack-shaped detail out of client-facing payloads.
		logger.ErrorContext(ctx, "request failed", "error", err, "code", code)
	} else {
		logger.WarnContext(ctx, "request rejected", "error", err, "code", code)
	}

	c.AbortWithStatusJSON(status, errorResponse{Error: ErrorBody{
		Code:      code,
		Message:   message,
		Fields:    fields,
		RequestID: observability.RequestIDFrom(ctx),
	}})
}
