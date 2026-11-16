package action

import "context"

type originKey struct{}

func WithOrigin(ctx context.Context, origin string) context.Context {
	return context.WithValue(ctx, originKey{}, origin)
}

func OriginFrom(ctx context.Context) string {
	origin, _ := ctx.Value(originKey{}).(string)
	return origin
}
