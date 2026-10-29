package llm

import "context"

type effortKey struct{}

// Efforts are the effort levels a run can ask for (same names as Claude Code).
var Efforts = []string{"low", "medium", "high", "xhigh", "max"}

// ValidEffort reports whether e is one of Efforts.
func ValidEffort(e string) bool {
	for _, k := range Efforts {
		if e == k {
			return true
		}
	}
	return false
}

// WithEffort returns ctx carrying the effort level for model calls made
// under it. Providers that support it read it with EffortFrom; the others
// ignore it.
func WithEffort(ctx context.Context, effort string) context.Context {
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, effortKey{}, effort)
}

// EffortFrom returns the effort level carried by ctx, or "".
func EffortFrom(ctx context.Context) string {
	e, _ := ctx.Value(effortKey{}).(string)
	return e
}
