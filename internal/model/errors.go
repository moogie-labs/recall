package model

import "errors"

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path"`
}

func (e *Error) Error() string {
	message := e.Message
	if message == "" {
		message = e.Code
	}
	if e.Path != "" {
		return e.Path + ": " + message
	}
	return message
}

// ErrorCode extracts a stable application code, including from wrapped errors.
// An empty code means no error; unexpected failures are internal errors.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var appError *Error
	if errors.As(err, &appError) && appError != nil && appError.Code != "" {
		return appError.Code
	}
	return "internal"
}
