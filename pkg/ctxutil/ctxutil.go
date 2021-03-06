package ctxutil // import "a4.io/blobstash/pkg/ctxutil"

import (
	"context"
	"net/http"
)

type key int

const (
	requestKey   key = 0
	namespaceKey key = 1
)

func WithNamespace(ctx context.Context, namespace string) context.Context {
	return context.WithValue(ctx, namespaceKey, namespace)
}

func Namespace(ctx context.Context) (string, bool) {
	namespace, ok := ctx.Value(namespaceKey).(string)
	return namespace, ok
}

func WithRequest(ctx context.Context, req *http.Request) context.Context {
	return context.WithValue(ctx, requestKey, req)
}

func Request(ctx context.Context) (*http.Request, bool) {
	req := ctx.Value(requestKey)
	if req != nil {
		return req.(*http.Request), true
	}
	return nil, false
}
