package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type captureRoundTripper struct {
	requests []capturedRequest
}

type capturedRequest struct {
	url     string
	payload map[string]any
}

func (c *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	_ = req.Body.Close()
	payload := map[string]any{}
	_ = json.Unmarshal(body, &payload)
	c.requests = append(c.requests, capturedRequest{url: req.URL.String(), payload: payload})

	respBody := `{"ok":true,"result":{"message_id":42}}`
	if strings.Contains(req.URL.Path, "sendPhoto") {
		respBody = `{"ok":true,"result":{"message_id":43}}`
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewBufferString(respBody)),
		Header:     make(http.Header),
	}, nil
}

func TestTelegramPayloadsUseHTMLParseMode(t *testing.T) {
	msg := telegramSendMessagePayload("chat", "<b>hello</b>")
	if msg["parse_mode"] != "HTML" {
		t.Fatalf("sendMessage parse_mode = %v", msg["parse_mode"])
	}

	photo := telegramSendPhotoPayload("chat", "https://example.com/img.png")
	if photo["parse_mode"] != "HTML" {
		t.Fatalf("sendPhoto parse_mode = %v", photo["parse_mode"])
	}
}

func TestSendTelegramPhotoLongCaptionFallsBackWithoutDoubleEscaping(t *testing.T) {
	rt := &captureRoundTripper{}
	client := &http.Client{Transport: rt}
	cfg := Config{TelegramToken: "token", TelegramChannelID: "channel"}
	longCaption := strings.Repeat("A", 1100) + " **Bold**"

	_, err := sendTelegramPhoto(context.Background(), client, cfg, "https://example.com/img.png", longCaption)
	if err != nil {
		t.Fatalf("sendTelegramPhoto error: %v", err)
	}
	if len(rt.requests) != 2 {
		t.Fatalf("expected 2 requests (photo + fallback message), got %d", len(rt.requests))
	}

	fallback := rt.requests[1]
	if !strings.Contains(fallback.url, "sendMessage") {
		t.Fatalf("expected fallback sendMessage call, got %s", fallback.url)
	}
	if fallback.payload["parse_mode"] != "HTML" {
		t.Fatalf("fallback parse_mode = %v", fallback.payload["parse_mode"])
	}
	text, _ := fallback.payload["text"].(string)
	if !strings.Contains(text, "<b>Bold</b>") {
		t.Fatalf("fallback text should contain HTML bold, got: %q", text)
	}
	if strings.Contains(text, "&lt;b&gt;Bold&lt;/b&gt;") {
		t.Fatalf("fallback text was double-escaped: %q", text)
	}
}
