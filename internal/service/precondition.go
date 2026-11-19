package service

import (
	"context"

	"jailops.net/jailops/sdk/api"
)

type expectUUIDKey struct{}

// WithExpectedUUID says which incarnation of an object the caller read (the UUID of its ETag): a change or delete of an
// object with another UUID fails with a conflict. The generation of an object counts from 1 again when an object of the
// same name is created anew, so a generation alone cannot tell the object a caller read from the one that replaced it.
func WithExpectedUUID(ctx context.Context, uuid string) context.Context {
	if uuid == "" {
		return ctx
	}
	return context.WithValue(ctx, expectUUIDKey{}, uuid)
}

// checkIncarnation fails with a conflict when the caller named a UUID and the object has another one.
func checkIncarnation(ctx context.Context, what, name, have string) error {
	want, _ := ctx.Value(expectUUIDKey{}).(string)
	if want == "" || want == have {
		return nil
	}
	return api.Errorf(api.CodePreconditionFailed, "%s %q is not the object you read: it was created again since (uuid %s, you read %s)", what, name, have, want).
		WithHint("read it again before you change or delete it").WithDetail("uuid", have)
}
