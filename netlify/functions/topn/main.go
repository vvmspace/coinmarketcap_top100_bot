package main

import (
	"context"
	"log"
	"os"

	"coinmarketcap_top100_bot/bot"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func handler(ctx context.Context) (events.APIGatewayProxyResponse, error) {
	log.Printf("[topn.handler] invocation started")
	log.Printf("[topn.handler] env presence: CMC_API_KEY=%t TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN=%t TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID=%t MONGODB_CONNECTION_STRING=%t GEMINI_API_KEY=%t",
		os.Getenv("CMC_API_KEY") != "",
		os.Getenv("TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN") != "",
		os.Getenv("TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID") != "",
		os.Getenv("MONGODB_CONNECTION_STRING") != "",
		os.Getenv("GEMINI_API_KEY") != "",
	)

	cfg, err := bot.ConfigFromEnv(false, false)
	if err != nil {
		log.Printf("[topn.handler] config validation failed: %v", err)
		return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
	}
	log.Printf("[topn.handler] config loaded successfully: top_n=%d ai_enabled=%t ai_provider=%s ai_model=%s", cfg.TopN, cfg.AIEnabled, cfg.AIProvider, cfg.AIModel)
	convert := os.Getenv("CONVERT")
	if convert == "" {
		convert = "USD"
	}
	log.Printf("[topn.handler] executing RunOnce with convert=%s", convert)
	if err := bot.RunOnce(ctx, cfg, bot.RunOptions{DryRun: false, NotifyExits: false, Convert: convert}); err != nil {
		log.Printf("[topn.handler] RunOnce failed: %v", err)
		return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
	}
	log.Printf("[topn.handler] invocation completed successfully")
	return events.APIGatewayProxyResponse{StatusCode: 200, Body: "ok"}, nil
}

func main() {
	lambda.Start(handler)
}
