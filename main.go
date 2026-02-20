package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"coinmarketcap_top100_bot/bot"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "print final message without sending")
	notifyExits := flag.Bool("notify-exits", false, "include exited coins in context")
	convert := flag.String("convert", "USD", "currency for market cap")
	skipMongo := flag.Bool("skip-mongo", false, "test posting flow without MongoDB state/history")
	testMessage := flag.String("test-message", "", "custom message for posting flow test (works with --skip-mongo)")
	testImageURL := flag.String("test-image-url", "", "optional image URL for --test-message")
	flag.Parse()

	cfg, err := bot.ConfigFromEnv(*dryRun, *skipMongo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := bot.RunOnce(context.Background(), cfg, bot.RunOptions{DryRun: *dryRun, NotifyExits: *notifyExits, Convert: *convert, SkipMongo: *skipMongo, TestMessage: *testMessage, TestImageURL: *testImageURL}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
