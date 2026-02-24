package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

func TestHandlerDocsEndpoint(t *testing.T) {
	resp, err := handler(context.Background(), events.APIGatewayProxyRequest{Path: "/api/docs"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if got := resp.Headers["Content-Type"]; !strings.Contains(got, "text/html") {
		t.Fatalf("unexpected content type: %s", got)
	}
	if !strings.Contains(resp.Body, "/swagger.json") {
		t.Fatalf("docs page does not reference swagger.json path: %s", resp.Body)
	}
}

func TestHandlerSwaggerEndpoint(t *testing.T) {
	resp, err := handler(context.Background(), events.APIGatewayProxyRequest{Path: "/api/v1/swagger.json"})
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("unexpected status: %d, body: %s", resp.StatusCode, resp.Body)
	}
	if got := resp.Headers["Content-Type"]; got != "application/json" {
		t.Fatalf("unexpected content type: %s", got)
	}
	if !strings.Contains(resp.Body, "openapi") {
		t.Fatalf("swagger response does not look like OpenAPI json")
	}
}
