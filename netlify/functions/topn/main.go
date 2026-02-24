package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"coinmarketcap_top100_bot/bot"
	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
)

func handler(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
	if req.Path == "/api/docs" || req.Path == "/api/v1/docs" {
		return events.APIGatewayProxyResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "text/html; charset=utf-8"},
			Body:       swaggerUIPage(req.Path),
		}, nil
	}

	if req.Path == "/swagger.json" || req.Path == "/api/v1/swagger.json" {
		content, err := readSwaggerSpec()
		if err != nil {
			return events.APIGatewayProxyResponse{StatusCode: 500, Body: `{"error":"unable to load swagger"}`}, nil
		}
		return events.APIGatewayProxyResponse{
			StatusCode: 200,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       string(content),
		}, nil
	}

	if req.HTTPMethod == "POST" && req.Path == "/api/v1/tick" {
		cfg, err := bot.ConfigFromEnv(false, false)
		if err != nil {
			return jsonError(500, err), nil
		}
		convert := os.Getenv("CONVERT")
		if convert == "" {
			convert = "USD"
		}
		text, msgID, err := bot.ReplayLastTick(ctx, cfg, convert)
		if err != nil {
			return jsonError(500, err), nil
		}
		payload := map[string]any{"ok": true, "text": text}
		if msgID != nil {
			payload["message_id"] = *msgID
		}
		body, _ := json.Marshal(payload)
		return events.APIGatewayProxyResponse{StatusCode: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: string(body)}, nil
	}

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

func readSwaggerSpec() ([]byte, error) {
	paths := []string{"docs/swagger.json", "../../../docs/swagger.json"}
	for _, p := range paths {
		content, err := os.ReadFile(p)
		if err == nil {
			return content, nil
		}
	}
	return nil, os.ErrNotExist
}

func swaggerUIPage(path string) string {
	jsonPath := "/api/v1/swagger.json"
	if path == "/api/docs" {
		jsonPath = "/swagger.json"
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>CoinMarketCap Top100 Bot API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.ui = SwaggerUIBundle({
      url: '%s',
      dom_id: '#swagger-ui'
    });
  </script>
</body>
</html>`, jsonPath)
}

func jsonError(status int, err error) events.APIGatewayProxyResponse {
	body, _ := json.Marshal(map[string]string{"error": err.Error()})
	return events.APIGatewayProxyResponse{StatusCode: status, Headers: map[string]string{"Content-Type": "application/json"}, Body: string(body)}
}

func main() {
	lambda.Start(handler)
}
