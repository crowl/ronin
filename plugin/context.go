package plugin

import "context"

type hostKey struct{}
type parentKey struct{}

// NewContext returns a context carrying host. Engine layers publish through
// FromContext, so installing the host on the root context enables plugins for
// everything derived from it.
func NewContext(ctx context.Context, host *Host) context.Context {
	return context.WithValue(ctx, hostKey{}, host)
}

// FromContext returns the host installed by NewContext, or nil when absent.
// The nil host is a valid no-op Host.
func FromContext(ctx context.Context) *Host {
	host, _ := ctx.Value(hostKey{}).(*Host)
	return host
}

// WithParent records the operation that encloses work performed under ctx.
// Operations started with NewOperation from the returned context report it
// as their ParentID.
func WithParent(ctx context.Context, operationID string) context.Context {
	return context.WithValue(ctx, parentKey{}, operationID)
}

// ParentID returns the enclosing operation recorded by WithParent, or "".
func ParentID(ctx context.Context) string {
	id, _ := ctx.Value(parentKey{}).(string)
	return id
}

// Begin allocates an operation under the parent recorded in ctx and returns a
// context in which it is the parent, for nesting child operations.
func Begin(ctx context.Context) (context.Context, Operation) {
	op := NewOperation(ctx)
	return WithParent(ctx, op.ID), op
}
