// Package geminiapp implements the native Gemini App ("Robin") HTTP/2 protocol
// used by Gemini mobile clients. It is a Go port of the wire, prompt and OAuth
// layers of ShawTim/gemini-app2api (MIT). No Google binaries, device identities
// or account credentials are bundled here.
package geminiapp

import (
	"errors"
	"fmt"
)

// Error is a protocol error with an upstream-compatible code and HTTP status.
type Error struct {
	Code   string
	Status int
	// Detail is optional diagnostic context; it never contains credentials.
	Detail string
}

func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// NewError builds a protocol error. status defaults to 400 when zero.
func NewError(code string, status int) *Error {
	if status == 0 {
		status = 400
	}
	return &Error{Code: code, Status: status}
}

// NewErrorf builds a protocol error with diagnostic detail.
func NewErrorf(code string, status int, format string, args ...any) *Error {
	err := NewError(code, status)
	err.Detail = fmt.Sprintf(format, args...)
	return err
}

// AsError reports whether err is (or wraps) a *Error.
func AsError(err error) (*Error, bool) {
	var target *Error
	if err != nil && errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
