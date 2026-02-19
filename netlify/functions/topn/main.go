package main

import (
	"context"
	"os"

	"coinmarketcap_top100_bot/bot"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func handler(ctx context.Context) (events.APIGatewayProxyResponse, error) {
	cfg, err := bot.ConfigFromEnv(false)
	if err != nil {
		return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
	}
	convert := os.Getenv("CONVERT")
	if convert == "" {
		convert = "USD"
	}
	if err := bot.RunOnce(ctx, cfg, bot.RunOptions{DryRun: false, NotifyExits: false, Convert: convert}); err != nil {
		return events.APIGatewayProxyResponse{StatusCode: 500, Body: err.Error()}, nil
	}
	return events.APIGatewayProxyResponse{StatusCode: 200, Body: "ok"}, nil
}

func main() {
	lambda.Start(handler)
}
