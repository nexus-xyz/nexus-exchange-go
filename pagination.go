package nexus

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"maps"
	"net/url"

	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// listPage is one page of a cursor-paginated list, in either of the two
// shapes the API serves:
//
//   - a bare JSON array, with the next cursor in the X-Next-Cursor response
//     header (frozen on the five operations that already had it), or
//   - the body envelope {"items": [...], "next_cursor": ...} that newer list
//     endpoints use.
//
// Callers of [paginate] never see which one an endpoint uses.
type listPage[T any] struct {
	items    []T
	next     string
	envelope bool // next came from the body, so the header is ignored
}

func (p *listPage[T]) UnmarshalJSON(b []byte) error {
	p.envelope = false
	if b = bytes.TrimLeft(b, " \t\r\n"); len(b) > 0 && b[0] == '[' {
		return transport.UnmarshalNumbers(b, &p.items)
	}
	var env struct {
		Items      []T     `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := transport.UnmarshalNumbers(b, &env); err != nil {
		return err
	}
	p.items, p.envelope = env.Items, true
	if env.NextCursor != nil {
		p.next = *env.NextCursor
	}
	return nil
}

// paginate walks a cursor-paginated GET, yielding every item of every page
// in order. It stops after the page that names no next cursor, on the first
// error (yielded once, with a zero T), or when the caller breaks.
//
// The cursor is opaque: it is copied from one response into the next
// request's cursor parameter byte for byte, never parsed or built. query is
// not modified; its limit, if any, is sent unvalidated on every page.
func paginate[T any](ctx context.Context, c *Client, path string, query url.Values) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		q := maps.Clone(query)
		if q == nil {
			q = url.Values{}
		}
		for {
			var p listPage[T]
			header, err := c.t.GetPage(ctx, path, q, &p)
			if err == nil && !p.envelope {
				p.next = header
			}
			if err != nil {
				var zero T
				yield(zero, err)
				return
			}
			for _, item := range p.items {
				if !yield(item, nil) {
					return
				}
			}
			if p.next == "" {
				return
			}
			if p.next == q.Get("cursor") {
				// A server handing back the cursor it was given would loop
				// forever. Comparing two opaque strings is not parsing one.
				var zero T
				yield(zero, fmt.Errorf("nexus: GET %s: server returned the cursor it was sent", path))
				return
			}
			q.Set("cursor", p.next)
		}
	}
}
