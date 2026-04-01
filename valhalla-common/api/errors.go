package api

import "errors"

var (
	ErrNotFound          = errors.New("not found")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrForbidden         = errors.New("forbidden")
	ErrConflict          = errors.New("resource already exists")
	ErrInvalidInput      = errors.New("invalid input")
	ErrInternalServer    = errors.New("internal server error")
	ErrNodeOffline       = errors.New("node is offline")
	ErrNoRouteAvailable  = errors.New("no route available")
	ErrSTUNFailed        = errors.New("STUN resolution failed")
	ErrRelayUnavailable  = errors.New("no relay nodes available")
	ErrTokenExpired      = errors.New("token expired")
	ErrInvalidToken      = errors.New("invalid token")
	ErrAccountDisabled   = errors.New("account disabled")
)

// APIError is a structured error returned by the REST API.
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return e.Message
}

func NewAPIError(code int, msg string) *APIError {
	return &APIError{Code: code, Message: msg}
}
