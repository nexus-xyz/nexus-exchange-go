package nexus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsServer serves POST /ws/token and upgrades GET /ws and GET /stream,
// running script on each accepted connection (n counts from 0). Returning
// from script kills the connection without a close frame, as the public load
// balancer does.
func wsServer(t *testing.T, script func(n int, r *http.Request, c *websocket.Conn)) (*Client, *atomic.Int32) {
	t.Helper()
	var conns, minted atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /ws/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"token":"tok-%d","expires_at":1757350060000}`, minted.Add(1))
	})
	upgrade := func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		script(int(conns.Add(1))-1, r, c)
	}
	mux.HandleFunc("GET /ws", upgrade)
	mux.HandleFunc("GET /stream", upgrade)
	return testClient(t, mux.ServeHTTP), &minted
}

func readFrame(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_, b, err := c.Read(context.Background())
	if err != nil {
		t.Error(err)
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Error(err)
	}
	return m
}

func send(c *websocket.Conn, frame string) {
	_ = c.Write(context.Background(), websocket.MessageText, []byte(frame))
}

// waitClose blocks until the client closes the connection.
func waitClose(c *websocket.Conn) { _, _, _ = c.Read(context.Background()) }

func next(t *testing.T, n interface {
	Next(context.Context) (Event, error)
}) Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ev, err := n.Next(ctx)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return ev
}

// TestSubscriptionResumesAndResyncs kills the account socket mid-stream and
// checks the reconnect mints a fresh token and resumes each (channel, market)
// from its own cursor; then induces an out_of_sync and checks it surfaces as
// a typed event and a fresh subscribe.
func TestSubscriptionResumesAndResyncs(t *testing.T) {
	subscribes := make(chan map[string]any, 16)
	tokens := make(chan string, 4)
	c, minted := wsServer(t, func(n int, r *http.Request, conn *websocket.Conn) {
		tokens <- r.URL.Query().Get("token")
		for range 2 {
			subscribes <- readFrame(t, conn)
		}
		switch n {
		case 0:
			send(conn, `{"op":"subscribed","channel":"fills","market":null,"seq_at_join":41}`)
			send(conn, `{"op":"event","channel":"fills","market":null,"seq":42,"payload":{"id":"f1","price":"1.50"}}`)
			send(conn, `{"op":"subscribed","channel":"trades","market":"BTC-USDX-PERP","seq_at_join":7}`)
			// Return: the connection dies with no close frame.
		case 1:
			send(conn, `{"op":"out_of_sync","channel":"fills","market":null,"oldest_seq":100}`)
			subscribes <- readFrame(t, conn)
			send(conn, `{"op":"event","channel":"fills","market":null,"seq":101,"payload":{}}`)
			waitClose(conn)
		}
	})
	ctx := context.Background()
	sub, err := c.Subscribe(ctx, ChannelFills, ChannelTrades("BTC-USDX-PERP"))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sub.s.minDelay = time.Millisecond

	want := func(got, want Event) {
		t.Helper()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("event = %#v, want %#v", got, want)
		}
	}
	want(next(t, sub), Subscribed{ChannelFills, 41})
	ev := next(t, sub).(ChannelEvent)
	if ev.Channel != ChannelFills || ev.Seq != 42 || string(ev.Payload) != `{"id":"f1","price":"1.50"}` {
		t.Fatalf("event = %+v", ev)
	}
	want(next(t, sub), Subscribed{ChannelTrades("BTC-USDX-PERP"), 7})
	if _, ok := next(t, sub).(Disconnected); !ok {
		t.Fatal("want Disconnected after the kill")
	}
	want(next(t, sub), Reconnected{})

	first := map[string]any{}
	for range 2 {
		f := <-subscribes
		if f["since"] != nil {
			t.Errorf("first connection sent since: %v", f)
		}
		first[f["channel"].(string)] = f
	}
	for range 2 {
		f := <-subscribes
		// Each resumes from its own cursor, not one global counter.
		wantSince := map[string]float64{"fills": 42, "trades": 7}[f["channel"].(string)]
		if f["op"] != "subscribe" || f["since"] != wantSince {
			t.Errorf("resubscribe = %v, want since %v", f, wantSince)
		}
	}

	want(next(t, sub), OutOfSync{ChannelFills, 100})
	if f := <-subscribes; f["channel"] != "fills" || f["since"] != nil {
		t.Errorf("after out_of_sync the SDK sent %v, want a fresh subscribe", f)
	}
	if ev := next(t, sub).(ChannelEvent); ev.Seq != 101 {
		t.Fatalf("event = %+v", ev)
	}
	if t1, t2 := <-tokens, <-tokens; t1 == t2 || minted.Load() != 2 {
		t.Errorf("tokens %q, %q (minted %d): want a fresh token per connection", t1, t2, minted.Load())
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.Next(ctx); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next after Close = %v", err)
	}
}

// TestOutOfSyncWithoutMarket: the server names no market when a connection
// falls behind its broadcast, so every market of the channel resyncs.
func TestOutOfSyncWithoutMarket(t *testing.T) {
	w := &Subscription{
		subs:    map[Channel]bool{ChannelBook("A"): true, ChannelBook("B"): true, ChannelFills: true},
		cursors: map[Channel]uint64{ChannelBook("A"): 5, ChannelBook("B"): 6, ChannelFills: 7},
	}
	evs := w.decode(context.Background(), []byte(`{"op":"out_of_sync","channel":"book","market":null,"oldest_seq":null}`))
	if len(evs) != 2 {
		t.Fatalf("events = %v, want one OutOfSync per book market", evs)
	}
	if len(w.cursors) != 1 || w.cursors[ChannelFills] != 7 {
		t.Fatalf("cursors = %v, want only fills left", w.cursors)
	}
}

// TestWireNamesAreSnakeCase fails if any op tag or channel name on either
// socket is not snake_case (R2.23): a camelCase name would silently never
// match on the server.
func TestWireNamesAreSnakeCase(t *testing.T) {
	snake := regexp.MustCompile(`^[a-z]+(_[a-z]+)*$`)
	names := []string{
		string(opSubscribe), string(opUnsubscribe),
		opSubscribed, opUnsubscribed, opEvent, opOutOfSync, opError,
		streamBook, streamTrades, streamMarketStatus,
	}
	for n := range channelNames {
		names = append(names, n)
	}
	var keys map[string]any
	b, _ := json.Marshal(clientFrame{Op: opSubscribe, Channel: "book", Market: "M", Since: new(uint64)})
	_ = json.Unmarshal(b, &keys)
	for k := range keys {
		names = append(names, k)
	}
	for _, n := range names {
		if !snake.MatchString(n) {
			t.Errorf("%q is not snake_case", n)
		}
	}
	w := &Subscription{subs: map[Channel]bool{ChannelFills: true}, cursors: map[Channel]uint64{}}
	if _, ok := w.decode(context.Background(), []byte(`{"op":"outOfSync","channel":"fills"}`))[0].(Unrecognized); !ok {
		t.Error("a camelCase op was accepted")
	}
}

// TestMarketStreamReconnects kills /stream mid-stream and checks the same
// single subscribe is sent again, and that frames decode exactly.
func TestMarketStreamReconnects(t *testing.T) {
	subscribes := make(chan string, 4)
	c, _ := wsServer(t, func(n int, r *http.Request, conn *websocket.Conn) {
		if r.Header.Get("X-Nexus-Api-Version") != APIVersion() || r.Header.Get("User-Agent") == "" {
			t.Errorf("upgrade headers = %v", r.Header)
		}
		_, b, _ := conn.Read(context.Background())
		subscribes <- string(b)
		switch n {
		case 0:
			send(conn, `{"type":"BookUpdate","market_id":"BTC-USDX-PERP","book":{"bids":[{"price":"64000.5","quantity":"0.25","order_count":2}],"asks":[],"sequence":15995}}`)
			send(conn, `{"type":"gap","missed":3}`)
		case 1:
			send(conn, `{"type":"Trade","market_id":"BTC-USDX-PERP","trade":{"id":"t1","price":64000.5,"amount":0.1}}`)
			send(conn, `{"type":"BookUpdate","market_id":"M","book":{"bids":[["1","2"]],"asks":[],"sequence":1}}`)
			waitClose(conn)
		}
	})
	m, err := c.MarketStream(context.Background(), StreamBook("BTC-USDX-PERP"), StreamTrades("BTC-USDX-PERP"), StreamBook("BTC-USDX-PERP"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.s.minDelay = time.Millisecond

	book := next(t, m).(BookSnapshot)
	if book.Sequence != 15995 || book.Bids[0].Price.String() != "64000.5" || book.Bids[0].OrderCount != 2 {
		t.Fatalf("book = %+v", book)
	}
	if g := next(t, m); g != (Gap{3}) {
		t.Fatalf("event = %#v, want Gap{3}", g)
	}
	if _, ok := next(t, m).(Disconnected); !ok {
		t.Fatal("want Disconnected")
	}
	if ev := next(t, m); ev != (Reconnected{}) {
		t.Fatalf("event = %#v", ev)
	}
	if tr := next(t, m).(TradePrint); *tr.Trade.Id != "t1" || tr.Trade.Price.String() != "64000.5" {
		t.Fatalf("trade = %+v", tr)
	}
	if _, ok := next(t, m).(Unrecognized); !ok {
		t.Fatal("a drifted book level must not decode as an empty book")
	}
	const sub = `{"subscribe":["book:BTC-USDX-PERP","trades:BTC-USDX-PERP"]}`
	if a, b := <-subscribes, <-subscribes; a != sub || b != sub {
		t.Fatalf("subscribes = %s, %s; want %s twice", a, b, sub)
	}
}

func TestMarketStreamRefused(t *testing.T) {
	c, _ := wsServer(t, func(_ int, _ *http.Request, conn *websocket.Conn) {
		_, _, _ = conn.Read(context.Background())
		send(conn, `{"type":"error","code":"unknown_channel","message":"unknown channel"}`)
		_ = conn.Close(websocket.StatusPolicyViolation, "unknown_channel")
	})
	m, err := c.MarketStream(context.Background(), StreamBook("*"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Next(context.Background()); err == nil {
		t.Fatal("want the refusal as an error")
	}
	if _, err := m.Next(context.Background()); !errors.Is(err, ErrStreamClosed) {
		t.Fatalf("Next after refusal = %v, want ErrStreamClosed", err)
	}
}

// TestConnectionCap: the SDK bounds its own connections, across both sockets.
func TestConnectionCap(t *testing.T) {
	c, _ := wsServer(t, func(_ int, _ *http.Request, conn *websocket.Conn) { waitClose(conn) })
	ctx := context.Background()
	var open []interface{ Close() error }
	defer func() {
		for _, s := range open {
			s.Close()
		}
	}()
	for i := range maxConnections {
		var s interface{ Close() error }
		var err error
		if i%2 == 0 {
			s, err = c.MarketStream(ctx, StreamBook("*"))
		} else {
			s, err = c.Subscribe(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, s)
	}
	if _, err := c.MarketStream(ctx, StreamBook("*")); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("stream %d: err = %v, want ErrTooManyConnections", maxConnections+1, err)
	}
	if _, err := c.Subscribe(ctx); !errors.Is(err, ErrTooManyConnections) {
		t.Fatalf("subscription %d: err = %v, want ErrTooManyConnections", maxConnections+1, err)
	}
	open[0].Close()
	s, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatalf("after Close: %v", err)
	}
	open[0] = s
}

func TestStreamsRefuseLocally(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("request sent: %s", r.URL) })
	for _, chs := range [][]StreamChannel{nil, {{}}, {StreamBook("")}, {StreamTrades("BTC USDX")}} {
		if _, err := c.MarketStream(ctx, chs...); err == nil {
			t.Errorf("MarketStream(%v) succeeded", chs)
		}
	}
	if _, err := c.Subscribe(ctx); err == nil {
		t.Error("Subscribe without credentials succeeded")
	}
	signed := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("request sent: %s", r.URL) })
	for _, ch := range []Channel{{}, ChannelBook(""), ChannelTrades("a b")} {
		if _, err := signed.Subscribe(ctx, ch); err == nil {
			t.Errorf("Subscribe(%v) succeeded", ch)
		}
	}
	secret, err := NewAPISecret(strings.Repeat("00", 32))
	if err != nil {
		t.Fatal(err)
	}
	mainnet, _ := NewClient(Mainnet, WithHMACAuth("k", secret))
	if _, err := mainnet.MarketStream(ctx, StreamBook("*")); !errors.Is(err, ErrMainnetNotTargetable) {
		t.Errorf("mainnet MarketStream: %v", err)
	}
	if _, err := mainnet.Subscribe(ctx); !errors.Is(err, ErrMainnetNotTargetable) {
		t.Errorf("mainnet Subscribe: %v", err)
	}
}

func TestSocketURLs(t *testing.T) {
	for network, want := range map[Network]string{
		Testnet: "wss://api.testnet.nexus.xyz/v1",
		Mainnet: "wss://api.nexus.xyz/v1",
		Local:   "ws://localhost:9090",
	} {
		c, err := NewClient(network)
		if err != nil {
			t.Fatal(err)
		}
		if got := c.t.WSURL(marketDataPath); got != want+"/stream" {
			t.Errorf("%v /stream = %s", network, got)
		}
		if got := c.t.WSURL("/ws"); got != want+"/ws" {
			t.Errorf("%v /ws = %s", network, got)
		}
	}
}
