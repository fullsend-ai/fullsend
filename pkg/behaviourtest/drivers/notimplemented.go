// Package drivers holds definitions shared by the behaviour-test driver
// packages (scm, ci, install).
package drivers

import (
	"errors"
	"fmt"
)

// ErrNotImplemented marks a driver operation that the selected driver
// does not support yet. Drivers wrap it in a *NotImplementedError naming
// the driver and the operation, so callers detect it with errors.Is and
// the message says exactly what is missing. A driver returns it instead
// of falling back to another forge's behaviour.
var ErrNotImplemented = errors.New("not implemented")

// NotImplementedError is the error a stub driver returns for an
// unsupported operation. It unwraps to ErrNotImplemented.
type NotImplementedError struct {
	// Driver names the driver, e.g. "forgejo SCM".
	Driver string
	// Operation names the unsupported operation, e.g. "CreateIssue".
	Operation string
}

func (e *NotImplementedError) Error() string {
	return fmt.Sprintf("%s driver: %s: %v", e.Driver, e.Operation, ErrNotImplemented)
}

// Unwrap lets errors.Is(err, ErrNotImplemented) match.
func (e *NotImplementedError) Unwrap() error { return ErrNotImplemented }

// NotImplemented returns a *NotImplementedError for operation of driver.
func NotImplemented(driver, operation string) error {
	return &NotImplementedError{Driver: driver, Operation: operation}
}
