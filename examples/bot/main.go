// Command bot is a minimal quoting bot for testnet. It keeps one bid and one
// ask resting a few ticks outside the best prices, cancels and requotes them
// as the book moves, and cancels everything on the way out.
//
// It shows what every bot on the Exchange needs:
//
//   - Market data from the public WebSocket ([nexus.Client.MarketStream]),
//     not REST polling.
//   - Cancel-on-disconnect. It keys on the account socket
//     ([nexus.Client.Subscribe]), so the bot opens that socket first, holds it
//     for its whole life, and reads its order and fill events.
//   - A clean shutdown. On ctrl-C, SIGTERM or -duration it cancels its
//     resting orders over REST and checks that none remain before it closes
//     the sockets. Cancel-on-disconnect is the backstop for a crash, not the
//     plan for a normal exit.
//
// Run it with an API key minted on testnet, the secret exactly as issued:
//
//	NEXUS_TESTNET_KEY_ID=... NEXUS_TESTNET_KEY_SECRET=... \
//	  go run ./examples/bot -market BTC-USDX-PERP -duration 1m
//
// -local points it at an indexer on this machine instead. The account needs
// collateral to quote. This is a demo of the SDK,
// not a strategy: it has no inventory, position or risk logic.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	nexus "github.com/nexus-xyz/nexus-exchange-go"
)

func main() {
	market := flag.String("market", "BTC-USDX-PERP", "market id to quote")
	offset := flag.Int64("offset", 50, "how many ticks outside the best bid and ask to quote")
	every := flag.Duration("every", 5*time.Second, "minimum time between requotes")
	duration := flag.Duration("duration", 0, "stop after this long (0: run until ctrl-C)")
	local := flag.Bool("local", false, "talk to an indexer on this machine instead of testnet")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *duration)
		defer cancel()
	}
	network := nexus.Testnet
	if *local {
		network = nexus.Local
	}
	if err := run(ctx, network, *market, *offset, *every); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, network nexus.Network, market string, offset int64, every time.Duration) error {
	secret, err := nexus.NewAPISecret(os.Getenv("NEXUS_TESTNET_KEY_SECRET"))
	if err != nil {
		return err
	}
	c, err := nexus.NewClient(network, nexus.WithHMACAuth(os.Getenv("NEXUS_TESTNET_KEY_ID"), secret))
	if err != nil {
		return err
	}
	q, err := newQuoter(ctx, c, market, offset)
	if err != nil {
		return err
	}

	// 1. The account socket, first: cancel-on-disconnect only covers the
	// account while it is open.
	sub, err := c.Subscribe(ctx, nexus.ChannelOrders, nexus.ChannelFills)
	if err != nil {
		return fmt.Errorf("open account socket: %w", err)
	}
	defer sub.Close()
	go logAccount(ctx, sub)

	// 2. The safety net. Active, not just Enabled, says it will fire.
	cod, err := c.Account().SetCancelOnDisconnect(ctx, true)
	if err != nil {
		return fmt.Errorf("enable cancel-on-disconnect: %w", err)
	}
	if !cod.Active {
		log.Print("warning: cancel-on-disconnect is enabled but not active (the exchange-side switch is off); a crash would leave quotes resting")
	}

	// 3. Market data.
	ms, err := c.MarketStream(ctx, nexus.StreamBook(market))
	if err != nil {
		return fmt.Errorf("open market stream: %w", err)
	}
	defer ms.Close()
	books := make(chan nexus.BookSnapshot, 1)
	go readBooks(ctx, ms, books)

	log.Printf("quoting %s %d ticks outside the touch; ctrl-C to stop", market, offset)
	var last time.Time
	for loop := true; loop; {
		select {
		case <-ctx.Done():
			loop = false
		case b := <-books:
			if time.Since(last) >= every {
				last = time.Now()
				q.requote(ctx, b)
			}
		}
	}

	// ctx is done, so shut down on a fresh one.
	sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return q.shutdown(sctx)
}

// quoter holds the bot's two quotes in one market.
type quoter struct {
	c        *nexus.Client
	market   string
	offset   *big.Rat
	decimals int
	size     nexus.Decimal
	resting  []string // ids of our quotes
}

func newQuoter(ctx context.Context, c *nexus.Client, market string, offset int64) (*quoter, error) {
	markets, err := c.FetchMarkets(ctx)
	if err != nil {
		return nil, fmt.Errorf("read markets: %w", err)
	}
	for _, m := range markets {
		if m.MarketId == nil || *m.MarketId != market || m.TickSize == nil {
			continue
		}
		size := m.MinOrderSize
		if size == nil {
			size = m.LotSize
		}
		if size == nil {
			return nil, fmt.Errorf("market %s serves no min_order_size or lot_size", market)
		}
		tick := m.TickSize.Rat()
		return &quoter{
			c:        c,
			market:   market,
			offset:   new(big.Rat).Mul(tick, new(big.Rat).SetInt64(offset)),
			decimals: decimals(m.TickSize.String()),
			size:     *size,
		}, nil
	}
	return nil, fmt.Errorf("market %s not found", market)
}

// requote cancels the resting quotes and places a new bid and ask offset
// ticks outside b's best prices. Errors are logged, not fatal: the next book
// tries again.
func (q *quoter) requote(ctx context.Context, b nexus.BookSnapshot) {
	if len(b.Bids) == 0 || len(b.Asks) == 0 {
		log.Print("book has an empty side; not quoting")
		return
	}
	q.cancelResting(ctx)

	var reqs []nexus.OrderRequest
	if r, ok := q.quote(nexus.Buy, b.Bids[0].Price); ok {
		reqs = append(reqs, r)
	}
	if r, ok := q.quote(nexus.Sell, b.Asks[0].Price); ok {
		reqs = append(reqs, r)
	}

	// One request for both quotes: a batch of up to 39 costs one unit.
	results, err := q.c.CreateOrders(ctx, reqs)
	if err != nil {
		log.Printf("place quotes: %v", err)
		return
	}
	for i, r := range results {
		switch outcome, _ := r.Discriminator(); outcome {
		case "ok":
			if ok, err := r.AsOrderResultOk(); err == nil && ok.Order.Id != nil {
				q.resting = append(q.resting, *ok.Order.Id)
				log.Printf("quoted %s %s @ %s (%s)", reqs[i].Side, reqs[i].Quantity, reqs[i].Price, *ok.Order.Id)
			}
		case "err":
			if rej, err := r.AsOrderResultErr(); err == nil {
				log.Printf("quote %s rejected: %s: %s", reqs[i].Side, rej.Error, rej.Message)
			}
		}
	}
}

// quote is a post-only order offset ticks outside best: below it for a bid,
// above it for an ask. ok is false when a bid would not be positive.
//
// The arithmetic is exact, on the served decimals, never float64. best is on
// the tick grid and so is the offset, so the quote is too, and FloatString
// renders it at the tick's precision without going through a float.
func (q *quoter) quote(side nexus.OrderSide, best nexus.Decimal) (nexus.OrderRequest, bool) {
	p := best.Rat()
	if side == nexus.Buy {
		p.Sub(p, q.offset)
	} else {
		p.Add(p, q.offset)
	}
	if p.Sign() <= 0 {
		return nexus.OrderRequest{}, false
	}
	px, err := nexus.ParseDecimal(p.FloatString(q.decimals))
	if err != nil {
		return nexus.OrderRequest{}, false
	}
	return nexus.OrderRequest{
		MarketId:    q.market,
		Side:        side,
		OrderType:   nexus.OrderTypeLimit,
		TimeInForce: nexus.PostOnly, // rejected rather than cross the book
		Price:       &px,
		Quantity:    q.size,
	}, true
}

// cancelResting cancels our quotes one by one. Cancels are never delayed by
// the SDK's pacing. A 404 means the order already filled or was cancelled.
func (q *quoter) cancelResting(ctx context.Context) {
	for _, id := range q.resting {
		if _, err := q.c.CancelOrder(ctx, id, q.market); err != nil && !errors.Is(err, nexus.ErrNotFound) {
			log.Printf("cancel %s: %v", id, err)
		}
	}
	q.resting = nil
}

// shutdown cancels every resting order in the market and confirms that none
// remain. A short cancel list proves nothing, so it reads the open orders
// back.
func (q *quoter) shutdown(ctx context.Context) error {
	cancelled, err := q.c.CancelAllOrders(ctx, q.market)
	if err != nil {
		return fmt.Errorf("shutdown: cancel all in %s: %w", q.market, err)
	}
	open, err := q.c.FetchOpenOrders(ctx)
	if err != nil {
		return fmt.Errorf("shutdown: read open orders: %w", err)
	}
	left := 0
	for _, o := range open {
		if o.MarketId != nil && *o.MarketId == q.market {
			left++
		}
	}
	if left > 0 {
		return fmt.Errorf("shutdown: %d orders still resting in %s", left, q.market)
	}
	log.Printf("shutdown: cancelled %d, none resting in %s", len(cancelled), q.market)
	return nil
}

// readBooks keeps the latest book snapshot in books, dropping stale ones.
func readBooks(ctx context.Context, ms *nexus.MarketStream, books chan nexus.BookSnapshot) {
	for {
		ev, err := ms.Next(ctx)
		if err != nil {
			return
		}
		switch e := ev.(type) {
		case nexus.BookSnapshot:
			select {
			case <-books:
			default:
			}
			books <- e
		case nexus.Disconnected:
			// Routine: the public hosts end every socket after about 30s.
			log.Printf("market stream disconnected (%v); reconnecting", e.Err)
		}
	}
}

// logAccount reads the account socket. It must be read for as long as it is
// open; its events are only logged here.
func logAccount(ctx context.Context, sub *nexus.Subscription) {
	for {
		ev, err := sub.Next(ctx)
		if err != nil {
			return
		}
		switch e := ev.(type) {
		case nexus.ChannelEvent:
			log.Printf("%s #%d: %s", e.Channel, e.Seq, e.Payload)
		case nexus.OutOfSync:
			// A real bot re-reads the channel's state over REST here.
			log.Printf("%s out of sync; re-read it over REST", e.Channel)
		case nexus.Disconnected:
			log.Printf("account socket disconnected (%v); the cancel-on-disconnect grace window is running", e.Err)
		case nexus.Reconnected:
			log.Print("account socket reconnected")
		}
	}
}

// decimals is the number of fraction digits in a decimal literal.
func decimals(s string) int {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}
