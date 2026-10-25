package agent

import "context"

type requestAuthorizationKey struct{}
type requestAuthorization struct{ Text, Scope string }

// RequestAuthorization exposes server-supplied scope and the current user's
// direct message to tools. Model arguments and quoted history cannot set it.
func RequestAuthorization(ctx context.Context) (text, scope string) {
	value, _ := ctx.Value(requestAuthorizationKey{}).(requestAuthorization)
	return value.Text, value.Scope
}
