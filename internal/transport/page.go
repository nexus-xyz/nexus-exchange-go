package transport

import (
	"context"
	"net/http"
	"net/url"
)

// headerSink is the context key do uses to hand a response's headers back to
// GetPage without changing the signature every other request goes through.
type headerSink struct{}

// GetPage is [Transport.Get] for a cursor-paginated list: it also returns the
// X-Next-Cursor header of the response that answered, empty on the last page
// or on error.
func (t *Transport) GetPage(ctx context.Context, path string, query url.Values, out any) (string, error) {
	var h http.Header
	err := t.Get(context.WithValue(ctx, headerSink{}, &h), path, query, out)
	if err != nil {
		return "", err
	}
	return h.Get("X-Next-Cursor"), nil
}
