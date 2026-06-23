package cli

import "fmt"

// exitError carries a specific process exit code for main.go's exitCoder.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

func (e *exitError) ExitCode() int { return e.code }

func newExitError(code int, format string, args ...any) *exitError {
	return &exitError{code: code, msg: fmt.Sprintf(format, args...)}
}

// Auth exit codes for fullsend auth check.
const (
	AuthExitBlocked       = 10
	AuthExitStaleOrUnauth = 11
)
