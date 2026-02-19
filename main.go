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
	flag.Parse()

	cfg, err := bot.ConfigFromEnv(*dryRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := bot.RunOnce(context.Background(), cfg, bot.RunOptions{DryRun: *dryRun, NotifyExits: *notifyExits, Convert: *convert}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
