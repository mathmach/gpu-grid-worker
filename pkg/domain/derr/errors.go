package derr

import (
	stderrors "errors"
	"fmt"
)

type Code string

const (
	CodeValidation             Code = "VALIDATION_ERROR"
	CodeInvalidCredentials     Code = "INVALID_CREDENTIALS"
	CodeUnauthorized           Code = "UNAUTHORIZED"
	CodeForbidden              Code = "FORBIDDEN"
	CodeIllegalState           Code = "ILLEGAL_STATE_TRANSITION"
	CodeConflict               Code = "CONFLICT"
	CodeRateLimited            Code = "RATE_LIMITED"
	CodeCircuitOpen            Code = "CIRCUIT_OPEN"
	CodeUpstreamFailure        Code = "UPSTREAM_FAILURE"
	CodeNoVulkanGPU            Code = "NO_VULKAN_GPU"
	CodeDigestMismatch         Code = "DIGEST_MISMATCH"
	CodePathTraversal          Code = "PATH_TRAVERSAL"
	CodeUnsupportedModelFormat Code = "UNSUPPORTED_MODEL_FORMAT"
	CodeMethodNotAllowed       Code = "METHOD_NOT_ALLOWED"
	CodeNotFound               Code = "NOT_FOUND"
	CodeInternal               Code = "INTERNAL_ERROR"
)

type AppError struct {
	Code       Code
	Message    string
	HTTPStatus int
	Params     map[string]any
	Cause      error
}

func (e *AppError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil && e.Message == "" {
		return e.Cause.Error()
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func New(code Code, status int, msg string, params map[string]any) *AppError {
	return &AppError{
		Code:       code,
		Message:    msg,
		HTTPStatus: status,
		Params:     params,
	}
}

func Wrap(code Code, status int, msg string, err error) *AppError {
	return &AppError{
		Code:       code,
		Message:    msg,
		HTTPStatus: status,
		Cause:      err,
	}
}

func AsAppError(err error) (*AppError, bool) {
	var target *AppError
	if stderrors.As(err, &target) && target != nil {
		return target, true
	}
	return nil, false
}

func StatusOf(err error) int {
	if appErr, ok := AsAppError(err); ok && appErr.HTTPStatus > 0 {
		return appErr.HTTPStatus
	}
	return 400
}

func Validationf(code Code, format string, args ...any) *AppError {
	return &AppError{
		Code:       code,
		Message:    fmt.Sprintf(format, args...),
		HTTPStatus: 400,
	}
}
