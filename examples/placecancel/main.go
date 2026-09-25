// Command placecancel places one post-only limit order on testnet and
// cancels it, with an API key:
//
//	NEXUS_TESTNET_KEY_ID=... NEXUS_TESTNET_KEY_SECRET=... \
//	  go run ./examples/placecancel -market BTC-USDX-PERP -price 1000 -quantity 0.001
//
// Pick a price well away from the market so the order rests: post-only means
// the exchange rejects it rather than let it take liquidity. The price and
// quantity must be multiples of the market's tick_size and lot_size (see
// Client.FetchMarkets).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	nexus "github.com/nexus-xyz/nexus-exchange-go"
)

func main() {
	market := flag.String("market", "BTC-USDX-PERP", "market id")
	price := flag.String("price", "", "limit price, a decimal string (required)")
	quantity := flag.String("quantity", "", "order size, a decimal string (required)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := run(ctx, *market, *price, *quantity); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, market, price, quantity string) error {
	// Prices and sizes are decimal strings end to end, never float64.
	px, err := nexus.ParseDecimal(price)
	if err != nil {
		return fmt.Errorf("-price: %w", err)
	}
	qty, err := nexus.ParseDecimal(quantity)
	if err != nil {
		return fmt.Errorf("-quantity: %w", err)
	}

	// The secret is the hex text exactly as issued; NewAPISecret decodes it.
	secret, err := nexus.NewAPISecret(os.Getenv("NEXUS_TESTNET_KEY_SECRET"))
	if err != nil {
		return err
	}
	c, err := nexus.NewClient(nexus.Testnet, nexus.WithHMACAuth(os.Getenv("NEXUS_TESTNET_KEY_ID"), secret))
	if err != nil {
		return err
	}

	placed, err := c.CreateOrder(ctx, nexus.OrderRequest{
		MarketId:    market,
		Side:        nexus.Buy,
		OrderType:   nexus.OrderTypeLimit,
		TimeInForce: nexus.PostOnly,
		Price:       &px,
		Quantity:    qty,
	})
	if err != nil {
		// Sent once, never retried: on a timeout the order may still exist.
		// Check FetchOpenOrders before placing it again.
		return fmt.Errorf("place: %w", err)
	}
	if placed.Order == nil || placed.Order.Id == nil {
		return fmt.Errorf("place: response carried no order id")
	}
	id := *placed.Order.Id
	fmt.Printf("placed %s: buy %s %s @ %s\n", id, qty, market, px)

	cancelled, err := c.CancelOrder(ctx, id, market)
	if err != nil {
		return fmt.Errorf("cancel %s: %w", id, err)
	}
	status := "?"
	if cancelled.Status != nil {
		status = string(*cancelled.Status)
	}
	fmt.Printf("cancelled %s: status %s\n", id, status)
	return nil
}
