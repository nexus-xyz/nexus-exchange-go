// Command marketdata reads public market data from testnet with no
// credentials: one ticker over REST, then book snapshots from the public
// WebSocket (/stream) until it has printed five, 30 seconds pass, or ctrl-C.
//
//	go run ./examples/marketdata -market BTC-USDX-PERP
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	nexus "github.com/nexus-xyz/nexus-exchange-go"
)

func main() {
	market := flag.String("market", "BTC-USDX-PERP", "market id")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := run(ctx, *market); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, market string) error {
	// No credential option: public market data needs none.
	c, err := nexus.NewClient(nexus.Testnet)
	if err != nil {
		return err
	}

	t, err := c.Ticker(ctx, market)
	if err != nil {
		return err
	}
	// CCXT-shaped fields are nullable json.Number: null before the first trade.
	last, _ := t.Last.Get()
	fmt.Printf("%s ticker: last %q\n", market, last)

	s, err := c.MarketStream(ctx, nexus.StreamBook(market))
	if err != nil {
		return err
	}
	defer s.Close()

	for n := 0; n < 5; {
		ev, err := s.Next(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%d book snapshots for %s in 30s (is the market trading?)", n, market)
		}
		if err != nil {
			return err
		}
		switch e := ev.(type) {
		case nexus.BookSnapshot:
			// A full top-20 book: replace any local copy, never merge.
			n++
			fmt.Printf("book seq %d: best bid %s, best ask %s\n", e.Sequence, best(e.Bids), best(e.Asks))
		case nexus.Disconnected:
			// Routine on the public hosts, which end every socket after ~30s.
			log.Printf("disconnected (%v); Next reconnects", e.Err)
		case nexus.Gap:
			log.Printf("server dropped %d frames; the next snapshot heals the book", e.Missed)
		}
	}
	return nil
}

func best(side []nexus.BookLevel) string {
	if len(side) == 0 {
		return "none"
	}
	return side[0].Quantity.String() + " @ " + side[0].Price.String()
}
