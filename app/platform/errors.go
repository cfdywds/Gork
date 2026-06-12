package platform

import "strconv"

// ErrorKind is the OpenAI-compatible error type string.
type ErrorKind string

const (
	ErrorKindValidation     ErrorKind = "invalid_request_error"
	ErrorKindAuthentication ErrorKind = "authentication_error"
	ErrorKindRateLimit      ErrorKind = "rate_limit_exceeded"
	ErrorKindUpstream       ErrorKind = "upstream_error"
	ErrorKindServer         ErrorKind = "server_error"
)

// AppError is the base error for application failures.
type AppError struct {
	Message string
	Kind    ErrorKind
	Code    string
	Status  int
	Details map[string]any
}

// NewAppError creates an application error.
func NewAppError(message string, kind ErrorKind, code string, status int, details map[string]any) *AppError {
	if kind == "" {
		kind = ErrorKindServer
	}
	if code == "" {
		code = "internal_error"
	}
	if status == 0 {
		status = 500
	}
	if details == nil {
		details = map[string]any{}
	}
	return &AppError{
		Message: message,
		Kind:    kind,
		Code:    code,
		Status:  status,
		Details: details,
	}
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ToDict returns the OpenAI-compatible JSON error body.
func (e *AppError) ToDict() map[string]any {
	err := map[string]any{
		"message": e.Message,
		"type":    e.Kind,
		"code":    e.Code,
	}
	if param, ok := e.Details["param"]; ok {
		err["param"] = param
	}
	return map[string]any{"error": err}
}

// ValidationError represents invalid request data.
type ValidationError struct {
	*AppError
	Param string
}

// NewValidationError creates a validation error.
func NewValidationError(message, param, code string) *ValidationError {
	if code == "" {
		code = "invalid_value"
	}
	return &ValidationError{
		AppError: NewAppError(message, ErrorKindValidation, code, 400, map[string]any{"param": param}),
		Param:    param,
	}
}

func (e *ValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.AppError
}

// AuthError represents invalid or missing API credentials.
type AuthError struct {
	*AppError
}

// NewAuthError creates an authentication error.
func NewAuthError(message string) *AuthError {
	if message == "" {
		message = "Invalid or missing API key"
	}
	return &AuthError{AppError: NewAppError(message, ErrorKindAuthentication, "invalid_api_key", 401, nil)}
}

func (e *AuthError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.AppError
}

// RateLimitError represents unavailable account capacity.
type RateLimitError struct {
	*AppError
}

// NewRateLimitError creates a rate limit error.
func NewRateLimitError(message string) *RateLimitError {
	if message == "" {
		message = "No available accounts"
	}
	return &RateLimitError{AppError: NewAppError(message, ErrorKindRateLimit, "rate_limit_exceeded", 429, nil)}
}

func (e *RateLimitError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.AppError
}

// UpstreamError represents an upstream XAI/Grok failure.
type UpstreamError struct {
	*AppError
	Body string
}

// NewUpstreamError creates an upstream error.
func NewUpstreamError(message string, status int, body string) *UpstreamError {
	if status == 0 {
		status = 502
	}
	return &UpstreamError{
		AppError: NewAppError(message, ErrorKindUpstream, "upstream_error", status, map[string]any{"body": body}),
		Body:     body,
	}
}

func (e *UpstreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.AppError
}

// StreamIdleTimeout represents a streaming response idle timeout.
type StreamIdleTimeout struct {
	*AppError
	TimeoutSeconds float64
}

// NewStreamIdleTimeout creates a stream idle timeout error.
func NewStreamIdleTimeout(timeoutSeconds float64) *StreamIdleTimeout {
	value := strconv.FormatFloat(timeoutSeconds, 'f', -1, 64)
	return &StreamIdleTimeout{
		AppError:       NewAppError("Stream idle timeout after "+value+"s", ErrorKindUpstream, "stream_idle_timeout", 504, nil),
		TimeoutSeconds: timeoutSeconds,
	}
}

func (e *StreamIdleTimeout) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.AppError
}
