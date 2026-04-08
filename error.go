package goserv

import (
	"errors"
	"fmt"
	stdhttp "net/http"
)

// ProblemDetails is an RFC 7807 problem details response.
// It is the default error body produced by HttpError and DefaultErrorHandler.
// Content-Type is always "application/problem+json".
type ProblemDetails struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func (p *ProblemDetails) StatusCode() int { return p.Status }
func (p *ProblemDetails) Headers() map[string]string {
	return map[string]string{"Content-Type": "application/problem+json"}
}
func (p *ProblemDetails) Data() any { return p }

// problemDetails builds a ProblemDetails value for the given status and detail message.
func problemDetails(status int, detail string) *ProblemDetails {
	return &ProblemDetails{
		Type:   "about:blank",
		Title:  stdhttp.StatusText(status),
		Status: status,
		Detail: detail,
	}
}

// HttpError is an error that carries HTTP response metadata.
// It implements both error and Response, so a handler returning (T, error)
// can return an *HttpError as the error and the framework will use its status
// code and body directly.
//
//	return nil, http.ErrNotFound("user not found")
//	return nil, http.ErrNotFound("user not found").WithCause(dbErr)
//	return nil, http.ErrConflict("email taken").WithHeader("X-Reason", "duplicate")
type HttpError struct {
	status  int
	message string
	details any
	headers map[string]string
	cause   error
}

func (e *HttpError) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.message, e.cause)
	}
	return e.message
}

func (e *HttpError) Unwrap() error   { return e.cause }
func (e *HttpError) StatusCode() int { return e.status }
func (e *HttpError) Headers() map[string]string {
	h := map[string]string{"Content-Type": "application/problem+json"}
	for k, v := range e.headers {
		h[k] = v
	}
	return h
}
func (e *HttpError) Data() any {
	if e.details != nil {
		return e.details
	}
	return problemDetails(e.status, e.message)
}

// WithCause attaches an underlying error (visible via errors.Is/As, included in Error()).
func (e *HttpError) WithCause(cause error) *HttpError {
	e.cause = cause
	return e
}

// WithDetails overrides the response body. Default body is {"error": message}.
func (e *HttpError) WithDetails(details any) *HttpError {
	e.details = details
	return e
}

// WithHeader adds or replaces a response header.
func (e *HttpError) WithHeader(key, value string) *HttpError {
	if e.headers == nil {
		e.headers = make(map[string]string, 1)
	}
	e.headers[key] = value
	return e
}

func ErrBadRequest(message string) *HttpError {
	return &HttpError{status: 400, message: message}
}

func ErrUnauthorized(message string) *HttpError {
	return &HttpError{status: 401, message: message}
}

func ErrForbidden(message string) *HttpError {
	return &HttpError{status: 403, message: message}
}

func ErrNotFound(message string) *HttpError {
	return &HttpError{status: 404, message: message}
}

func ErrConflict(message string) *HttpError {
	return &HttpError{status: 409, message: message}
}

func ErrUnprocessableEntity(message string) *HttpError {
	return &HttpError{status: 422, message: message}
}

func ErrInternalServer(message string) *HttpError {
	return &HttpError{status: 500, message: message}
}

// ErrorHandler translates an error returned by a handler into a Response.
// Register a custom one via ServerBuilder.UseErrorHandler.
type ErrorHandler func(err error, ctx *Context) Response

// DefaultErrorHandler uses the error as the response when it is an *HttpError,
// and falls back to a generic 500 ProblemDetails for all other errors.
func DefaultErrorHandler(err error, _ *Context) Response {
	var httpErr *HttpError
	if errors.As(err, &httpErr) {
		return httpErr
	}
	return problemDetails(stdhttp.StatusInternalServerError, "internal server error")
}

// defaultErrorHandler is the handler set by NewServer when none is registered.
var defaultErrorHandler ErrorHandler = DefaultErrorHandler
