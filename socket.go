package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// maxConnections bounds the WebSocket connections one process holds through
// this SDK, [MarketStream] and [Subscription] together. The server caps
// connections per IP at 5 on /ws, and /stream has no cap at all yet (R7.5 is
// proposed, not built), so the SDK caps itself rather than lean on a server
// limit that may not exist.
const maxConnections = 5

// connSlots holds one token per open stream, from open until Close. It is
// per process, not per Client, because the server's cap is per IP.
var connSlots = make(chan struct{}, maxConnections)

var (
	// ErrTooManyConnections is returned when opening a stream while this
	// process already holds maxConnections (5) of them open. Close one first.
	// A stream holds its slot across reconnects, until Close.
	ErrTooManyConnections = errors.New("nexus: this process already holds 5 open WebSocket streams; Close one first")
	// ErrStreamClosed is returned by Next once the stream is closed.
	ErrStreamClosed = errors.New("nexus: stream closed")
)

// Event is one item from [MarketStream.Next] or [Subscription.Next]. The set
// is closed: switch on the concrete type. Each type's documentation says which
// socket sends it.
type Event interface{ event() }

// Disconnected reports that the socket dropped (or a reconnect attempt
// failed). The next call to Next waits out a jittered backoff and reconnects.
// Both sockets.
type Disconnected struct{ Err error }

// Reconnected reports that a new socket is open and every subscription was
// sent again. Anything published while disconnected may be missed: see each
// socket's documentation for how to resynchronise. Both sockets.
type Reconnected struct{}

// Unrecognized is a frame this SDK version could not decode: a new frame
// type, or a known one whose shape drifted. It is passed through, not
// dropped. Both sockets.
type Unrecognized struct {
	Frame []byte
	Err   error
}

func (Disconnected) event() {}
func (Reconnected) event()  {}
func (Unrecognized) event() {}

// healthyAfter is how long a connection must stay up to reset the backoff
// even if it delivered nothing: the public load balancer ends every socket
// about 30s after the upgrade, which must not ratchet the delay up.
const healthyAfter = 5 * time.Second

// socket is the reconnect loop both streams share. It is read by one
// goroutine (the caller of Next); Close and writes may come from others.
type socket struct {
	dial  func(context.Context) (*websocket.Conn, error)
	hello func(context.Context, *websocket.Conn) error // called with mu held

	// Backoff bounds. Fields so tests can shrink them.
	minDelay, maxDelay time.Duration

	mu     sync.Mutex
	conn   *websocket.Conn
	closed bool
	done   chan struct{}
	free   sync.Once

	// Read goroutine only.
	delay     time.Duration
	opened    time.Time
	delivered bool
}

// openSocket takes a connection slot and connects once, so a bad URL or
// credential fails here rather than inside Next.
func openSocket(ctx context.Context, s *socket) error {
	select {
	case connSlots <- struct{}{}:
	default:
		return ErrTooManyConnections
	}
	s.minDelay, s.maxDelay = 500*time.Millisecond, 30*time.Second
	s.done = make(chan struct{})
	if err := s.connect(ctx); err != nil {
		s.release()
		return err
	}
	return nil
}

func (s *socket) release() { s.free.Do(func() { <-connSlots }) }

func (s *socket) connect(ctx context.Context) error {
	conn, err := s.dial(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		conn.CloseNow()
		return ErrStreamClosed
	}
	if err := s.hello(ctx, conn); err != nil {
		conn.CloseNow()
		return err
	}
	s.conn, s.opened, s.delivered = conn, time.Now(), false
	return nil
}

// read returns the next data frame, or a lifecycle event, or a terminal
// error (ctx done, or closed).
func (s *socket) read(ctx context.Context) ([]byte, Event, error) {
	s.mu.Lock()
	conn, closed := s.conn, s.closed
	s.mu.Unlock()
	if closed {
		return nil, nil, ErrStreamClosed
	}
	if conn == nil {
		if err := s.wait(ctx); err != nil {
			return nil, nil, err
		}
		if err := s.connect(ctx); err != nil {
			switch {
			case errors.Is(err, ErrStreamClosed):
				return nil, nil, ErrStreamClosed
			case ctx.Err() != nil:
				return nil, nil, ctx.Err()
			}
			return nil, Disconnected{Err: err}, nil
		}
		return nil, Reconnected{}, nil
	}
	_, data, err := conn.Read(ctx)
	if err == nil {
		s.delivered = true
		return data, nil, nil
	}
	s.mu.Lock()
	s.conn = nil
	closed = s.closed
	s.mu.Unlock()
	conn.CloseNow()
	if s.delivered || time.Since(s.opened) >= healthyAfter {
		s.delay = 0
	}
	switch {
	case closed:
		return nil, nil, ErrStreamClosed
	case ctx.Err() != nil:
		return nil, nil, ctx.Err()
	}
	return nil, Disconnected{Err: err}, nil
}

// wait sleeps a full-jitter exponential backoff, so a fleet of clients spreads
// its reconnects instead of stampeding a recovering endpoint.
func (s *socket) wait(ctx context.Context) error {
	s.delay = min(max(2*s.delay, s.minDelay), s.maxDelay)
	t := time.NewTimer(rand.N(s.delay + 1))
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrStreamClosed
	}
}

func (s *socket) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	conn := s.conn
	s.conn = nil
	s.mu.Unlock()
	s.release()
	if conn != nil {
		conn.CloseNow()
	}
	return nil
}

// writeJSON sends v as one text frame.
func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}
