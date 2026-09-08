package llm

import (
	"errors"
	"fmt"
)

var ErrContentBlocked = errors.New("llm: upstream content blocked")

// ContentBlockedError retains protocol reasons independently of localized messages.
type ContentBlockedError struct {
	Provider Provider
	Stage    string
	Reason   string
	Message  string
}

func (e *ContentBlockedError) Error() string {
	detail := fmt.Sprintf("llm: %s content blocked (%s: %s)", e.Provider, e.Stage, e.Reason)
	if e.Message != "" {
		detail += ": " + e.Message
	}
	return detail
}
func (e *ContentBlockedError) Unwrap() error { return ErrContentBlocked }
