package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/coder/websocket"
	"github.com/nexus-xyz/nexus-exchange-go/internal/transport"
)

// marketDataPath is the socket keyless market data is read from. /stream is
// marked legacy in the spec, but it is the only socket that needs no token
// (/ws wants one even for public channels), and the Rust and TypeScript SDKs
// use it too. This constant is the one place to change if that moves.
const marketDataPath = "/stream"

// StreamChannel is a channel of the public market-data socket. Build one with
// [StreamBook], [StreamTrades] or [StreamMarketStatus]; the set is closed,
// because the server matches names as strings and a typo would subscribe to
// nothing. market is a market id ("BTC-USDX-PERP") or "*" for every market.
type StreamChannel struct{ kind, market string }

// The /stream channel prefixes, snake_case like everything on the wire.
const (
	streamBook         = "book"
	streamTrades       = "trades"
	streamMarketStatus = "market_status"
)

// StreamBook is book:<market>: full top-20 snapshots, as [BookSnapshot].
func StreamBook(market string) StreamChannel { return StreamChannel{streamBook, market} }

// StreamTrades is trades:<market>: public prints, as [TradePrint].
func StreamTrades(market string) StreamChannel { return StreamChannel{streamTrades, market} }

// StreamMarketStatus is market_status:<market>: [MarketHalted] and
// [MarketResumed].
func StreamMarketStatus(market string) StreamChannel {
	return StreamChannel{streamMarketStatus, market}
}

// String is the channel's wire name, for example "book:BTC-USDX-PERP".
func (c StreamChannel) String() string { return c.kind + ":" + c.market }

// MarketStream is the public market-data socket (GET /stream). It needs no
// credentials. Get one from [Client.MarketStream] and read it with Next from
// one goroutine; Close may be called from any.
//
// The protocol is small, and it shapes this API:
//
//   - The server reads exactly one message, the subscription, and never reads
//     again: there are no acknowledgements and no unsubscribe. The channel set
//     is fixed when the stream opens and is sent again on every reconnect;
//     open another stream to change it.
//   - Book frames are full top-20 snapshots. Replace the local book on every
//     [BookSnapshot]; never merge. Its Sequence is monotonic but skips values,
//     so a jump is not a lost update.
//   - Loss is reported, not replayed: [Gap] says the server dropped frames for
//     this connection. Books heal on the next snapshot (or re-read
//     [Client.FetchOrderBook]); trades do not, so re-read [Client.FetchTrades].
//
// On the public hosts the load balancer ends every socket about 30s after the
// upgrade. That is routine: Next returns [Disconnected], then [Reconnected].
// Trades published in between are missed; reconcile over REST after each
// Reconnected if every print matters.
type MarketStream struct{ s socket }

// Events a [MarketStream] returns, besides [Disconnected], [Reconnected] and
// [Unrecognized].
type (
	// BookLevel is one level of a [BookSnapshot]: an object on this socket,
	// unlike the [price, amount] pairs of [Client.FetchOrderBook].
	BookLevel struct {
		Price      Decimal `json:"price"`
		Quantity   Decimal `json:"quantity"`
		OrderCount uint32  `json:"order_count"`
	}
	// BookSnapshot is a full top-20 book for one market. Replace, never merge.
	BookSnapshot struct {
		MarketID   string
		Bids, Asks []BookLevel // best first
		// Sequence equals the REST order book's nonce for the same state. It
		// increases but is not contiguous: a jump is not a missed update.
		Sequence uint64
		// ReceivedAt is local arrival time; the server sends no timestamp.
		ReceivedAt time.Time
	}
	// TradePrint is one public trade. Price, amount and cost are JSON
	// numbers on this socket, kept as json.Number.
	TradePrint struct {
		MarketID string
		Trade    Trade
	}
	// MarketHalted says a market stopped trading. Timestamp is epoch ms.
	MarketHalted struct {
		MarketID, Reason string
		Timestamp        int64
	}
	// MarketResumed says a market is trading again. Timestamp is epoch ms.
	MarketResumed struct {
		MarketID  string
		Timestamp int64
	}
	// Gap says the server dropped up to Missed frames because this
	// connection fell behind. Missed counts every channel of every market,
	// not only this stream's. Nothing is replayed: see [MarketStream].
	Gap struct{ Missed uint64 }
)

func (BookSnapshot) event()  {}
func (TradePrint) event()    {}
func (MarketHalted) event()  {}
func (MarketResumed) event() {}
func (Gap) event()           {}

// MarketStream opens the public market-data socket and subscribes to
// channels. It connects before returning, so an unreachable host fails here.
//
// It returns an error, before any network I/O, for an empty channel set, a
// zero StreamChannel, an empty or whitespace-bearing market (the server would
// accept either and deliver nothing), a Mainnet client
// ([ErrMainnetNotTargetable]), or when this process already holds 5 streams
// ([ErrTooManyConnections]).
func (c *Client) MarketStream(ctx context.Context, channels ...StreamChannel) (*MarketStream, error) {
	msg, err := streamSubscribe(channels)
	if err != nil {
		return nil, err
	}
	if c.t.Refuse != nil {
		return nil, c.t.Refuse
	}
	url := c.t.WSURL(marketDataPath)
	m := &MarketStream{s: socket{
		dial: func(ctx context.Context) (*websocket.Conn, error) { return c.t.Dial(ctx, url) },
		hello: func(ctx context.Context, conn *websocket.Conn) error {
			return conn.Write(ctx, websocket.MessageText, msg)
		},
	}}
	if err := openSocket(ctx, &m.s); err != nil {
		return nil, err
	}
	return m, nil
}

// streamSubscribe builds the one message /stream reads, deduplicated in order.
func streamSubscribe(channels []StreamChannel) ([]byte, error) {
	if len(channels) == 0 {
		return nil, errors.New("nexus: MarketStream needs at least one channel; the server reads only the first message")
	}
	var names []string
	seen := map[StreamChannel]bool{}
	for _, ch := range channels {
		if ch.kind == "" {
			return nil, errors.New("nexus: zero StreamChannel; build one with StreamBook, StreamTrades or StreamMarketStatus")
		}
		if ch.market == "" || strings.ContainsFunc(ch.market, unicode.IsSpace) {
			return nil, fmt.Errorf("nexus: invalid /stream market %q: the server would silently match nothing", ch.market)
		}
		if !seen[ch] {
			seen[ch] = true
			names = append(names, ch.String())
		}
	}
	return json.Marshal(map[string][]string{"subscribe": names})
}

// Next returns the next event, reconnecting as needed: after [Disconnected],
// the next call waits out a backoff and returns [Reconnected] (or another
// Disconnected). It returns an error only when ctx is done, the stream is
// closed ([ErrStreamClosed]), or the server refused the subscription, which
// no reconnect can fix.
//
// Cancelling ctx closes the current socket; a later Next reconnects.
func (m *MarketStream) Next(ctx context.Context) (Event, error) {
	data, ev, err := m.s.read(ctx)
	if err != nil || ev != nil {
		return ev, err
	}
	ev, err = decodeStream(data, time.Now())
	if err != nil {
		m.s.close()
	}
	return ev, err
}

// Close closes the socket and frees its connection slot. Next then returns
// [ErrStreamClosed].
func (m *MarketStream) Close() error { return m.s.close() }

// decodeStream decodes one /stream frame. Frames are tagged by "type" with
// their fields inline.
func decodeStream(data []byte, at time.Time) (Event, error) {
	var f struct {
		Type     string `json:"type"`
		MarketID string `json:"market_id"`
		Book     *struct {
			Bids, Asks []BookLevel
			Sequence   uint64
		}
		Trade     *Trade
		Reason    string
		Timestamp int64
		Missed    uint64
		Code      string
		Message   string
	}
	if err := transport.UnmarshalNumbers(data, &f); err != nil {
		return Unrecognized{data, err}, nil
	}
	switch f.Type {
	case "BookUpdate":
		if f.Book != nil {
			return BookSnapshot{f.MarketID, f.Book.Bids, f.Book.Asks, f.Book.Sequence, at}, nil
		}
	case "Trade":
		if f.Trade != nil {
			return TradePrint{f.MarketID, *f.Trade}, nil
		}
	case "MarketHalted":
		return MarketHalted{f.MarketID, f.Reason, f.Timestamp}, nil
	case "MarketResumed":
		return MarketResumed{f.MarketID, f.Timestamp}, nil
	case "gap":
		return Gap{f.Missed}, nil
	case "error":
		// Sent once before a 1008 close when the subscription is invalid.
		return nil, fmt.Errorf("nexus: /stream refused the subscription: %s: %s", f.Code, f.Message)
	}
	return Unrecognized{data, fmt.Errorf("nexus: unrecognized /stream frame type %q", f.Type)}, nil
}
