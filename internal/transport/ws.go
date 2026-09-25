package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coder/websocket"
)

// WSURL is the socket URL for path on the same host and mount as the REST
// base, with the scheme swapped: https://api.testnet.nexus.xyz/v1 and
// "/stream" give wss://api.testnet.nexus.xyz/v1/stream. The sockets are
// routed under the edge prefix like every REST path; the host root answers
// 404 (ENG-17132).
func (t *Transport) WSURL(path string) string {
	u := t.base + path
	if s, ok := strings.CutPrefix(u, "https://"); ok {
		return "wss://" + s
	}
	return "ws://" + strings.TrimPrefix(u, "http://")
}

// Dial opens a WebSocket to rawURL through the transport's *http.Client, sending
// the same User-Agent and X-Nexus-Api-Version as every REST request. It
// refuses before any network I/O when Refuse is set.
func (t *Transport) Dial(ctx context.Context, rawURL string) (*websocket.Conn, error) {
	if t.Refuse != nil {
		return nil, t.Refuse
	}
	h := http.Header{}
	h.Set("User-Agent", t.userAgent)
	h.Set("X-Nexus-Api-Version", t.apiVersion)
	conn, resp, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{HTTPClient: t.http, HTTPHeader: h})
	if err != nil {
		if resp != nil {
			// The URL may carry a single-use token; name the status, not the URL.
			return nil, fmt.Errorf("nexus: websocket upgrade refused: HTTP %d", resp.StatusCode)
		}
		// net/http's *url.Error quotes the URL, token and all; keep only
		// its cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("nexus: websocket dial: %w", err)
	}
	conn.SetReadLimit(maxBody)
	return conn, nil
}
