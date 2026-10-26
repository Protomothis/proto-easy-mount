package provider

import "errors"

// UserError is an error worded for the person using the app: what went
// wrong and what to do about it, with the technical cause kept aside.
type UserError struct {
	Msg  string // what happened, in plain words
	Hint string // what to try next; may be empty
	Err  error  // technical cause; may be nil
}

func (e *UserError) Error() string {
	s := e.Msg
	if e.Hint != "" {
		s += " " + e.Hint
	}
	if e.Err != nil {
		s += " (" + e.Err.Error() + ")"
	}
	return s
}

func (e *UserError) Unwrap() error { return e.Err }

// Explain wraps err in a UserError.
func Explain(msg, hint string, err error) error {
	return &UserError{Msg: msg, Hint: hint, Err: err}
}

// AsUserError extracts a UserError, or describes err generically.
func AsUserError(err error) *UserError {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue
	}
	return &UserError{Msg: "문제가 생겼어요.", Err: err}
}

// ErrSignIn marks a session the server no longer accepts: only signing in
// again helps. Errors without it (no network, server down) are worth
// retrying.
var ErrSignIn = errors.New("sign in again")

// NeedsSignIn reports whether err means the session is gone.
func NeedsSignIn(err error) bool { return errors.Is(err, ErrSignIn) }
