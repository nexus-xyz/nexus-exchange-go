package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/coder/websocket"
)

// op is the tag of a client-to-server frame on /ws. It is unexported and has
// exactly two values, so the socket is read-only by construction (R2.20):
// orders go over REST, and sending any other op means changing this type, not
// passing a different string.
type op string

const (
	opSubscribe   op = "subscribe"
	opUnsubscribe op = "unsubscribe"
)

// The server-to-client op tags. The protocol is snake_case throughout (R2.23).
const (
	opSubscribed   = "subscribed"
	opUnsubscribed = "unsubscribed"
	opEvent        = "event"
	opOutOfSync    = "out_of_sync"
	opError        = "error"
)

type clientFrame struct {
	Op      op      `json:"op"`
	Channel string  `json:"channel"`
	Market  string  `json:"market,omitempty"`
	Since   *uint64 `json:"since,omitempty"`
}

// Channel is a channel of the account socket (/ws). Build a per-market one
// with [ChannelTrades], [ChannelBook] or [ChannelCandles], or use one of the
// account channels ([ChannelOrders] and the rest). The set is closed: the
// fields are unexported, so a misspelt channel cannot be written.
type Channel struct{ name, market string }

// channelNames is every /ws channel name, mapped to whether it takes a market.
var channelNames = map[string]bool{
	"trades": true, "book": true, "candles": true,
	"orders": false, "fills": false, "positions": false, "balances": false, "liquidations": false,
}

// The account channels, scoped to the account whose key minted the token.
// The server drops an account event published without an account rather
// than broadcast it (R2.22), so a missing event is not necessarily a bug.
var (
	ChannelOrders       = Channel{name: "orders"}
	ChannelFills        = Channel{name: "fills"}
	ChannelPositions    = Channel{name: "positions"}
	ChannelBalances     = Channel{name: "balances"}
	ChannelLiquidations = Channel{name: "liquidations"}
)

// ChannelTrades is the public trades channel for market.
func ChannelTrades(market string) Channel { return Channel{"trades", market} }

// ChannelBook is the public book channel for market.
func ChannelBook(market string) Channel { return Channel{"book", market} }

// ChannelCandles is the public candles channel for market.
func ChannelCandles(market string) Channel { return Channel{"candles", market} }

// Name is the channel's wire name, such as "fills".
func (c Channel) Name() string { return c.name }

// Market is the channel's market, empty for account channels.
func (c Channel) Market() string { return c.market }

func (c Channel) String() string {
	if c.market == "" {
		return c.name
	}
	return c.name + ":" + c.market
}

func (c Channel) validate() error {
	perMarket, ok := channelNames[c.name]
	switch {
	case !ok:
		return errors.New("nexus: zero Channel; use ChannelTrades, ChannelOrders and the like")
	case perMarket && (c.market == "" || strings.ContainsFunc(c.market, unicode.IsSpace)):
		return fmt.Errorf("nexus: invalid market %q for channel %s", c.market, c.name)
	}
	return nil
}

// Subscription is the account socket (GET /ws): account channels, and public
// ones if wanted, over one authenticated connection. Get one from
// [Client.Subscribe] and read it with Next from one goroutine; Subscribe,
// Unsubscribe and Close may be called from any.
//
// # Cancel-on-disconnect needs this socket
//
// Cancel-on-disconnect ([Account.SetCancelOnDisconnect]) keys on /ws: its
// grace window starts at the account's last /ws disconnect. It protects only a
// bot that holds a Subscription open. REST traffic and [MarketStream] do not
// count, and a bot that closes its Subscription (or never opens one) is not
// covered.
//
// # Resume and resynchronisation
//
// Each (channel, market) has its own sequence, seq. The Subscription records
// the highest seq seen for each (from events, and from the subscribed ack's
// seq_at_join) and, after a reconnect, subscribes again with since set to it,
// so the server replays what was missed. When the server no longer holds that
// history it answers out_of_sync; Next returns [OutOfSync], and the
// Subscription has already subscribed that channel again from the live edge.
// What was missed must then be re-read over REST, which the OutOfSync event
// documents.
//
// Every connection needs a fresh token from POST /ws/token (single use, 60s),
// so each reconnect mints one with the client's credentials.
type Subscription struct {
	s socket

	// Guarded by s.mu.
	subs    map[Channel]bool
	cursors map[Channel]uint64 // highest seq seen; absent means none

	pending []Event // read goroutine only
}

// Events a [Subscription] returns, besides [Disconnected], [Reconnected] and
// [Unrecognized].
type (
	// Subscribed acknowledges a subscribe. SeqAtJoin is the channel's seq when
	// it took effect.
	Subscribed struct {
		Channel   Channel
		SeqAtJoin uint64
	}
	// Unsubscribed acknowledges an unsubscribe.
	Unsubscribed struct{ Channel Channel }
	// ChannelEvent is one event on a channel. Payload is exactly as served;
	// Seq increases per (channel, market).
	ChannelEvent struct {
		Channel        Channel
		Seq            uint64
		EngineEnvelope *EngineEnvelope
		Payload        json.RawMessage
	}
	// OutOfSync says the server could not replay Channel's missed events, so
	// the local view of it is stale. Do not continue past it: the SDK has
	// already subscribed Channel again from the live edge, and the state must
	// be re-read over REST: orders [Client.FetchOpenOrders], fills [Client.FetchMyTrades],
	// positions [Client.FetchPositions], balances [Account.FetchBalance], book
	// [Client.FetchOrderBook], trades [Client.FetchTrades], candles [Client.FetchOHLCV].
	// liquidations has no REST read. OldestSeq is the oldest seq the server
	// still holds, 0 when it holds none.
	OutOfSync struct {
		Channel   Channel
		OldestSeq uint64
	}
	// ServerError is an error frame: an invalid op, an unknown channel, or a
	// limit such as subscription_limit_exceeded. The connection stays up.
	ServerError struct{ Message string }
	// EngineEnvelope is the matching engine's stamp on an event, when it has
	// one. It is informational; upstream engine loss is reported on /status,
	// not on this socket.
	EngineEnvelope struct {
		Epoch     uint64 `json:"epoch"`
		Sequence  uint64 `json:"sequence"`
		EmittedAt int64  `json:"emitted_at"`
	}
)

func (Subscribed) event()   {}
func (Unsubscribed) event() {}
func (ChannelEvent) event() {}
func (OutOfSync) event()    {}
func (ServerError) event()  {}

// wsTokenPath mints account socket tokens. Never the legacy /ws-tokens, whose
// token carries no account.
const wsTokenPath = "/ws/token"

// Subscribe mints a token, opens the account socket and subscribes to
// channels (none is fine; add them later with [Subscription.Subscribe]). It
// connects before returning, so a bad credential fails here.
//
// It needs [WithHMACAuth]. It returns an error before any network I/O for a
// client without credentials, an invalid channel, a Mainnet client
// ([ErrMainnetNotTargetable]), or when this process already holds 5 streams
// ([ErrTooManyConnections]).
//
// Cancel-on-disconnect protects the account only while a Subscription is
// open; see [Subscription].
func (c *Client) Subscribe(ctx context.Context, channels ...Channel) (*Subscription, error) {
	if c.t.Signer == nil {
		return nil, errors.New("nexus: Subscribe needs credentials to mint a /ws token; use WithHMACAuth")
	}
	if c.t.Refuse != nil {
		return nil, c.t.Refuse
	}
	w := &Subscription{subs: map[Channel]bool{}, cursors: map[Channel]uint64{}}
	for _, ch := range channels {
		if err := ch.validate(); err != nil {
			return nil, err
		}
		w.subs[ch] = true
	}
	base := c.t.WSURL("/ws")
	w.s.dial = func(ctx context.Context) (*websocket.Conn, error) {
		var tok struct {
			Token string `json:"token"`
		}
		if err := c.t.Send(ctx, http.MethodPost, wsTokenPath, nil, &tok); err != nil {
			return nil, err
		}
		if tok.Token == "" {
			return nil, errors.New("nexus: POST /ws/token returned no token")
		}
		return c.t.Dial(ctx, base+"?token="+url.QueryEscape(tok.Token))
	}
	w.s.hello = func(ctx context.Context, conn *websocket.Conn) error {
		for ch := range w.subs {
			if err := writeJSON(ctx, conn, w.frame(opSubscribe, ch)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := openSocket(ctx, &w.s); err != nil {
		return nil, err
	}
	return w, nil
}

// frame builds an op for ch, resuming from its cursor on subscribe. Called
// with s.mu held.
func (w *Subscription) frame(o op, ch Channel) clientFrame {
	f := clientFrame{Op: o, Channel: ch.name, Market: ch.market}
	if seq, ok := w.cursors[ch]; ok && o == opSubscribe {
		f.Since = &seq
	}
	return f
}

// Subscribe adds ch: it is sent now if connected, and again on every
// reconnect. An error means the send failed; ch stays subscribed and is sent
// on the next reconnect.
func (w *Subscription) Subscribe(ctx context.Context, ch Channel) error {
	if err := ch.validate(); err != nil {
		return err
	}
	return w.send(ctx, ch, func() { w.subs[ch] = true }, opSubscribe)
}

// Unsubscribe removes ch and forgets its cursor.
func (w *Subscription) Unsubscribe(ctx context.Context, ch Channel) error {
	return w.send(ctx, ch, func() { delete(w.subs, ch); delete(w.cursors, ch) }, opUnsubscribe)
}

func (w *Subscription) send(ctx context.Context, ch Channel, update func(), o op) error {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if w.s.closed {
		return ErrStreamClosed
	}
	update()
	if w.s.conn == nil {
		return nil
	}
	return writeJSON(ctx, w.s.conn, w.frame(o, ch))
}

// Next returns the next event, reconnecting as needed: after [Disconnected],
// the next call mints a token, reconnects, resubscribes every channel from its
// cursor and returns [Reconnected] (or another Disconnected). It returns an
// error only when ctx is done or the Subscription is closed
// ([ErrStreamClosed]).
//
// Cancelling ctx closes the current socket, which starts the
// cancel-on-disconnect grace window; a later Next reconnects.
func (w *Subscription) Next(ctx context.Context) (Event, error) {
	for len(w.pending) == 0 {
		data, ev, err := w.s.read(ctx)
		if err != nil || ev != nil {
			return ev, err
		}
		w.pending = w.decode(ctx, data)
	}
	ev := w.pending[0]
	w.pending = w.pending[1:]
	return ev, nil
}

// Close closes the socket and frees its connection slot. Next then returns
// [ErrStreamClosed]. Closing starts the cancel-on-disconnect grace window.
func (w *Subscription) Close() error { return w.s.close() }

func (w *Subscription) decode(ctx context.Context, data []byte) []Event {
	var f struct {
		Op             string          `json:"op"`
		Channel        string          `json:"channel"`
		Market         *string         `json:"market"`
		Seq            *uint64         `json:"seq"`
		SeqAtJoin      *uint64         `json:"seq_at_join"`
		OldestSeq      *uint64         `json:"oldest_seq"`
		Message        string          `json:"message"`
		EngineEnvelope *EngineEnvelope `json:"engine_envelope"`
		Payload        json.RawMessage `json:"payload"`
	}
	bad := func(err error) []Event { return []Event{Unrecognized{data, err}} }
	if err := json.Unmarshal(data, &f); err != nil {
		return bad(err)
	}
	if f.Op == opError {
		return []Event{ServerError{f.Message}}
	}
	ch := Channel{name: f.Channel}
	if f.Market != nil {
		ch.market = *f.Market
	}
	if _, ok := channelNames[ch.name]; !ok {
		return bad(fmt.Errorf("nexus: unrecognized /ws channel %q", f.Channel))
	}
	switch {
	case f.Op == opSubscribed && f.SeqAtJoin != nil:
		w.advance(ch, *f.SeqAtJoin)
		return []Event{Subscribed{ch, *f.SeqAtJoin}}
	case f.Op == opUnsubscribed:
		return []Event{Unsubscribed{ch}}
	case f.Op == opEvent && f.Seq != nil:
		w.advance(ch, *f.Seq)
		return []Event{ChannelEvent{ch, *f.Seq, f.EngineEnvelope, f.Payload}}
	case f.Op == opOutOfSync:
		var oldest uint64
		if f.OldestSeq != nil {
			oldest = *f.OldestSeq
		}
		return w.resync(ctx, ch, oldest)
	}
	return bad(fmt.Errorf("nexus: unrecognized /ws frame op %q", f.Op))
}

// advance records seq as ch's cursor if it is the highest seen. A frame for a
// channel no longer subscribed is not tracked.
func (w *Subscription) advance(ch Channel, seq uint64) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	if cur, ok := w.cursors[ch]; w.subs[ch] && (!ok || seq > cur) {
		w.cursors[ch] = seq
	}
}

// resync drops the cursor of every subscription the out_of_sync names and
// subscribes each again from the live edge: the server does not keep a
// subscription it could not backfill. The server may name no market for a
// per-market channel (when this connection fell behind its broadcast); every
// market of that channel is then resynchronised, each with its own event.
func (w *Subscription) resync(ctx context.Context, ch Channel, oldest uint64) []Event {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	var out []Event
	for sub := range w.subs {
		if sub.name != ch.name || (ch.market != "" && sub.market != ch.market) {
			continue
		}
		delete(w.cursors, sub)
		if w.s.conn != nil {
			// A failed send surfaces as a read error; the reconnect resends it.
			_ = writeJSON(ctx, w.s.conn, w.frame(opSubscribe, sub))
		}
		out = append(out, OutOfSync{sub, oldest})
	}
	return out
}
