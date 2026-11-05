package resource

import "errors"

// ErrForbidden means that only a direct administrator request may do this,
// such as submitting a durable operation; policy runs never may.
var ErrForbidden = errors.New("resource: only a direct administrator request is allowed")

// TypeError returns a sentinel error of a resource type that keeps the
// type's own text and matches kind, one of the shared errors of this
// package (ErrNotFound, ErrConflict, ErrPrecondition, ErrForbidden), through
// errors.Is. The API maps only the shared errors, so the errors of every
// type get the same response without the API naming the type.
func TypeError(text string, kind error) error { return &typeError{text: text, kind: kind} }

type typeError struct {
	text string
	kind error
}

func (e *typeError) Error() string { return e.text }
func (e *typeError) Unwrap() error { return e.kind }
