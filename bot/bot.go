package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const defaultPrompt = `%new_coins%
`

const defaultFallbackTemplate = `🚀 New entries in CoinMarketCap Top %top_n% (%convert%)

%EACH new_coins%• #%rank% %name% (%symbol%)%IF market_cap% — mcap: %market_cap%%END_IF%
%END_EACH%%IF exited_coins%
📉 Exited:
%EACH exited_coins%• #%rank% %name% (%symbol%)
%END_EACH%%END_IF%`

type RunOptions struct {
	DryRun       bool
	NotifyExits  bool
	Convert      string
	SkipMongo    bool
	TestMessage  string
	TestImageURL string
}

type Config struct {
	CMCAPIKey                string
	TelegramToken            string
	TelegramChannelID        string
	MongoDBConnectionString  string
	MongoDBDatabase          string
	MongoDBStateCollection   string
	MongoDBCoinsCollection   string
	MongoDBHistoryCollection string
	TopN                     int
	AIEnabled                bool
	AIProvider               string
	AIModel                  string
	GeminiAPIKey             string
}

func ConfigFromEnv(dryRun bool, skipMongo bool) (Config, error) {
	_ = godotenv.Load(".env")
	req := func(name string) (string, error) {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" {
			return "", fmt.Errorf("missing required env var %s", name)
		}
		return v, nil
	}
	optionalReq := func(name string) (string, error) {
		v := strings.TrimSpace(os.Getenv(name))
		if v == "" && !dryRun {
			return "", fmt.Errorf("missing required env var %s", name)
		}
		return v, nil
	}
	cmc, err := req("CMC_API_KEY")
	if err != nil {
		return Config{}, err
	}
	tgToken, err := optionalReq("TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN")
	if err != nil {
		return Config{}, err
	}
	tgChat, err := optionalReq("TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID")
	if err != nil {
		return Config{}, err
	}
	mongoURI := strings.TrimSpace(os.Getenv("MONGODB_CONNECTION_STRING"))
	if mongoURI == "" && !skipMongo {
		return Config{}, fmt.Errorf("missing required env var %s", "MONGODB_CONNECTION_STRING")
	}

	topN := 100
	if raw := strings.TrimSpace(os.Getenv("TOP_N")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return Config{}, errors.New("TOP_N must be a positive integer")
		}
		topN = n
	}
	geminiKey := strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
	aiEnabled := geminiKey != ""
	if raw := strings.TrimSpace(os.Getenv("AI_ENABLED")); raw != "" {
		aiEnabled = strings.EqualFold(raw, "true")
	}

	return Config{
		CMCAPIKey:                cmc,
		TelegramToken:            tgToken,
		TelegramChannelID:        tgChat,
		MongoDBConnectionString:  mongoURI,
		MongoDBDatabase:          envOr("MONGODB_DB", "cmc_top"),
		MongoDBStateCollection:   envOr("MONGODB_STATE_COLLECTION", "state"),
		MongoDBCoinsCollection:   envOr("MONGODB_COINS_COLLECTION", "coins"),
		MongoDBHistoryCollection: envOr("MONGODB_HISTORY_COLLECTION", "history"),
		TopN:                     topN,
		AIEnabled:                aiEnabled,
		AIProvider:               envOr("AI_PROVIDER", "gemini"),
		AIModel:                  envOr("AI_MODEL", "gemini-3-flash-preview"),
		GeminiAPIKey:             geminiKey,
	}, nil
}

func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

type Coin struct {
	ID                int64    `bson:"id" json:"id"`
	Name              string   `bson:"name" json:"name"`
	Symbol            string   `bson:"symbol" json:"symbol"`
	Rank              int64    `bson:"rank" json:"rank"`
	MarketCap         *float64 `bson:"market_cap,omitempty" json:"market_cap,omitempty"`
	MarketCapCurrency string   `bson:"market_cap_currency" json:"market_cap_currency"`
	ImageURL          string   `bson:"image_url,omitempty" json:"image_url,omitempty"`
}

type RecentPost struct {
	CreatedAtUTC   string `json:"created_at_utc"`
	Text           string `json:"text"`
	MentionedCoins []Coin `json:"mentioned_coins"`
}

type stateDoc struct {
	ID        string    `bson:"_id"`
	UpdatedAt time.Time `bson:"updated_at"`
	TopN      int64     `bson:"top_n"`
	Convert   string    `bson:"convert"`
	IDs       []int64   `bson:"ids"`
}

type stateCoinDoc struct {
	StateID string    `bson:"state_id"`
	ID      int64     `bson:"id"`
	Name    string    `bson:"name"`
	Symbol  string    `bson:"symbol"`
	Rank    int64     `bson:"rank"`
	Updated time.Time `bson:"updated_at"`
	Created time.Time `bson:"created_at,omitempty"`
	IsActive bool     `bson:"is_active"`

	MarketCap         *float64 `bson:"market_cap,omitempty"`
	MarketCapCurrency string   `bson:"market_cap_currency"`
	ImageURL          string   `bson:"image_url,omitempty"`
}

type historyDoc struct {
	CreatedAt         time.Time `bson:"created_at"`
	TopN              int64     `bson:"top_n"`
	Convert           string    `bson:"convert"`
	NewCoinIDs        []int64   `bson:"new_coin_ids"`
	Text              string    `bson:"text"`
	MentionedCoins    []Coin    `bson:"mentioned_coins"`
	TelegramMessageID *int64    `bson:"telegram_message_id,omitempty"`
}

func RunOnce(ctx context.Context, cfg Config, opt RunOptions) error {
	log.Printf("[RunOnce] start: top_n=%d convert=%s dry_run=%t notify_exits=%t skip_mongo=%t ai_enabled=%t ai_provider=%s", cfg.TopN, opt.Convert, opt.DryRun, opt.NotifyExits, opt.SkipMongo, cfg.AIEnabled, cfg.AIProvider)

	log.Printf("[RunOnce] step 1/11: creating HTTP client")
	httpClient := &http.Client{Timeout: 30 * time.Second}

	if opt.SkipMongo {
		return runWithoutMongo(ctx, httpClient, cfg, opt)
	}

	log.Printf("[RunOnce] step 2/11: connecting to MongoDB")
	db, client, err := connectDB(ctx, cfg)
	if err != nil {
		log.Printf("[RunOnce] failed to connect to MongoDB: %v", err)
		return err
	}
	defer client.Disconnect(context.Background())
	log.Printf("[RunOnce] connected to MongoDB database=%s", cfg.MongoDBDatabase)

	log.Printf("[RunOnce] step 3/11: fetching current top-%d from CoinMarketCap", cfg.TopN)
	current, err := fetchCMCTopN(ctx, httpClient, cfg, opt)
	if err != nil {
		log.Printf("[RunOnce] failed to fetch CoinMarketCap listings: %v", err)
		return err
	}
	log.Printf("[RunOnce] fetched %d current coins", len(current))
	log.Printf("Incoming top %d %v", cfg.TopN, coinSymbols(current))

	stateCollection := db.Collection(cfg.MongoDBStateCollection)
	coinsCollection := db.Collection(cfg.MongoDBCoinsCollection)
	historyCollection := db.Collection(cfg.MongoDBHistoryCollection)
	log.Printf("[RunOnce] using collections: state=%s coins=%s history=%s", cfg.MongoDBStateCollection, cfg.MongoDBCoinsCollection, cfg.MongoDBHistoryCollection)

	log.Printf("[RunOnce] step 4/11: loading previous state snapshot")
	var prev stateDoc
	err = stateCollection.FindOne(ctx, bson.M{"_id": "top"}).Decode(&prev)
	if errors.Is(err, mongo.ErrNoDocuments) {
		log.Printf("[RunOnce] previous state not found; writing baseline and exiting without Telegram post")
		return writeState(ctx, stateCollection, coinsCollection, cfg.TopN, opt.Convert, current)
	}
	if err != nil {
		log.Printf("[RunOnce] failed to load previous state: %v", err)
		return err
	}
	log.Printf("[RunOnce] loaded previous state with %d ids", len(prev.IDs))
	prevCoins, err := loadStateCoins(ctx, coinsCollection, "top")
	if err != nil {
		log.Printf("[RunOnce] failed to load state coins: %v", err)
		return err
	}
	log.Printf("From DB top %d %v", cfg.TopN, coinSymbols(prevCoins))

	log.Printf("[RunOnce] step 5/11: calculating diff between previous and current top lists")
	prevSet := map[int64]struct{}{}
	for _, id := range prev.IDs {
		prevSet[id] = struct{}{}
	}
	currentSet := map[int64]struct{}{}
	for _, c := range current {
		currentSet[c.ID] = struct{}{}
	}

	newCoins := make([]Coin, 0)
	for _, c := range current {
		if _, ok := prevSet[c.ID]; !ok {
			newCoins = append(newCoins, c)
		}
	}
	if len(newCoins) == 0 {
		log.Printf("[RunOnce] no new coins found; exiting without Telegram post")
		return nil
	}
	log.Printf("[RunOnce] detected %d new coin(s)", len(newCoins))

	exitedCoins := []Coin{}
	if opt.NotifyExits {
		for _, c := range prevCoins {
			if _, ok := currentSet[c.ID]; !ok {
				exitedCoins = append(exitedCoins, c)
			}
		}
		log.Printf("[RunOnce] notify exits enabled; detected %d exited coin(s)", len(exitedCoins))
	} else {
		log.Printf("[RunOnce] notify exits disabled; exited coins are not included")
	}

	log.Printf("[RunOnce] step 6/11: loading recent posts from history")
	recentPosts, err := loadRecentPosts(ctx, historyCollection)
	if err != nil {
		log.Printf("[RunOnce] failed to load recent posts: %v", err)
		return err
	}
	log.Printf("[RunOnce] loaded %d recent post(s)", len(recentPosts))

	log.Printf("[RunOnce] step 7/11: building render context")
	renderCtx := buildRenderContext(cfg, opt, newCoins, exitedCoins, recentPosts)

	log.Printf("[RunOnce] step 8/11: producing Telegram text")
	text, err := produceTelegramText(ctx, httpClient, cfg, renderCtx)
	if err != nil {
		log.Printf("[RunOnce] failed to produce Telegram text: %v", err)
		return err
	}
	log.Printf("[RunOnce] produced Telegram text with %d characters", len(text))

	if opt.DryRun {
		log.Printf("[RunOnce] step 9/11: dry-run enabled; printing message and exiting")
		fmt.Println(text)
		return nil
	}

	log.Printf("[RunOnce] step 10/11: sending Telegram message")
	msgID, err := sendTelegramMessage(ctx, httpClient, cfg, text, firstCoinImageURL(newCoins))
	if err != nil {
		log.Printf("[RunOnce] failed to send Telegram message: %v", err)
		return err
	}
	if msgID != nil {
		log.Printf("[RunOnce] Telegram message sent successfully: message_id=%d", *msgID)
	} else {
		log.Printf("[RunOnce] Telegram message sent successfully: message_id is unavailable")
	}

	log.Printf("[RunOnce] step 11/11: persisting state and writing history")
	if err := writeState(ctx, stateCollection, coinsCollection, cfg.TopN, opt.Convert, current); err != nil {
		log.Printf("[RunOnce] failed to write state: %v", err)
		return err
	}
	newIDs := make([]int64, 0, len(newCoins))
	for _, c := range newCoins {
		newIDs = append(newIDs, c.ID)
	}
	_, err = historyCollection.InsertOne(ctx, historyDoc{
		CreatedAt: time.Now().UTC(), TopN: int64(cfg.TopN), Convert: opt.Convert,
		NewCoinIDs: newIDs, Text: text, MentionedCoins: newCoins, TelegramMessageID: msgID,
	})
	if err != nil {
		log.Printf("[RunOnce] failed to append history: %v", err)
		return err
	}
	log.Printf("[RunOnce] completed successfully")
	return err
}

func runWithoutMongo(ctx context.Context, httpClient *http.Client, cfg Config, opt RunOptions) error {
	log.Printf("[RunOnce] skip-mongo mode enabled: testing posting flow without MongoDB")
	if strings.TrimSpace(opt.TestMessage) != "" {
		if opt.DryRun {
			fmt.Println(opt.TestMessage)
			return nil
		}
		msgID, err := sendTelegramMessage(ctx, httpClient, cfg, opt.TestMessage, strings.TrimSpace(opt.TestImageURL))
		if err != nil {
			return err
		}
		if msgID != nil {
			log.Printf("[RunOnce] skip-mongo custom test post sent: message_id=%d", *msgID)
		}
		return nil
	}

	current, err := fetchCMCTopN(ctx, httpClient, cfg, opt)
	if err != nil {
		return err
	}
	if len(current) == 0 {
		log.Printf("[RunOnce] no coins returned from CoinMarketCap in skip-mongo mode")
		return nil
	}
	newCount := len(current)
	if newCount > 3 {
		newCount = 3
	}
	newCoins := current[:newCount]
	renderCtx := buildRenderContext(cfg, opt, newCoins, []Coin{}, []RecentPost{})
	text, err := produceTelegramText(ctx, httpClient, cfg, renderCtx)
	if err != nil {
		return err
	}
	if opt.DryRun {
		fmt.Println(text)
		return nil
	}
	msgID, err := sendTelegramMessage(ctx, httpClient, cfg, text, firstCoinImageURL(newCoins))
	if err != nil {
		return err
	}
	if msgID != nil {
		log.Printf("[RunOnce] skip-mongo post sent: message_id=%d", *msgID)
	}
	return nil
}

func connectDB(ctx context.Context, cfg Config) (*mongo.Database, *mongo.Client, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoDBConnectionString).SetAppName("coinmarketcap_top100_bot"))
	if err != nil {
		return nil, nil, err
	}
	return client.Database(cfg.MongoDBDatabase), client, nil
}

func fetchCMCTopN(ctx context.Context, client *http.Client, cfg Config, opt RunOptions) ([]Coin, error) {
	u := fmt.Sprintf("https://pro-api.coinmarketcap.com/v1/cryptocurrency/listings/latest?start=1&limit=%d&convert=%s&sort=market_cap&sort_dir=desc", cfg.TopN, url.QueryEscape(opt.Convert))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("X-CMC_PRO_API_KEY", cfg.CMCAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cmc error: %s %s", resp.Status, string(b))
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	data, _ := payload["data"].([]any)
	coins := make([]Coin, 0, len(data))
	for _, item := range data {
		m, _ := item.(map[string]any)
		coin := Coin{ID: asInt64(m["id"]), Name: asStringDef(m["name"], "Unknown"), Symbol: asStringDef(m["symbol"], "???"), Rank: asInt64(m["cmc_rank"]), MarketCapCurrency: opt.Convert}
		if quote, ok := m["quote"].(map[string]any); ok {
			if curr, ok := quote[opt.Convert].(map[string]any); ok {
				if mc, ok := asFloat(curr["market_cap"]); ok {
					coin.MarketCap = &mc
				}
			}
		}
		coins = append(coins, coin)
	}
	logos, err := fetchCMCLogos(ctx, client, cfg, coins)
	if err != nil {
		log.Printf("[fetchCMCTopN] unable to fetch coin logos: %v", err)
		return coins, nil
	}
	for i := range coins {
		if logo := logos[coins[i].ID]; logo != "" {
			coins[i].ImageURL = logo
		}
	}
	return coins, nil
}

func fetchCMCLogos(ctx context.Context, client *http.Client, cfg Config, coins []Coin) (map[int64]string, error) {
	if len(coins) == 0 {
		return map[int64]string{}, nil
	}
	ids := make([]string, 0, len(coins))
	for _, c := range coins {
		ids = append(ids, strconv.FormatInt(c.ID, 10))
	}
	u := fmt.Sprintf("https://pro-api.coinmarketcap.com/v2/cryptocurrency/info?id=%s", strings.Join(ids, ","))
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("X-CMC_PRO_API_KEY", cfg.CMCAPIKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cmc info error: %s %s", resp.Status, string(b))
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	out := map[int64]string{}
	data, _ := payload["data"].(map[string]any)
	for k, raw := range data {
		id, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			continue
		}
		entry, _ := raw.(map[string]any)
		if logo := asString(entry["logo"]); logo != "" {
			out[id] = logo
		}
	}
	return out, nil
}

func loadRecentPosts(ctx context.Context, historyCollection *mongo.Collection) ([]RecentPost, error) {
	cur, err := historyCollection.Find(ctx, bson.M{}, options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(3))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []RecentPost{}
	for cur.Next(ctx) {
		var d historyDoc
		if err := cur.Decode(&d); err != nil {
			return nil, err
		}
		out = append(out, RecentPost{CreatedAtUTC: d.CreatedAt.UTC().Format(time.RFC3339), Text: d.Text, MentionedCoins: d.MentionedCoins})
	}
	return out, cur.Err()
}

func buildRenderContext(cfg Config, opt RunOptions, newCoins, exited []Coin, recent []RecentPost) map[string]any {
	return map[string]any{"project_name": "coinmarketcap_top100_bot", "timestamp_utc": time.Now().UTC().Format(time.RFC3339), "top_n": cfg.TopN, "convert": opt.Convert, "new_coins": newCoins, "exited_coins": exited, "recent_posts": recent}
}

func produceTelegramText(ctx context.Context, client *http.Client, cfg Config, renderCtx map[string]any) (string, error) {
	fallback := loadTemplateOrDefault("templates/telegram_post_fallback.template.md", defaultFallbackTemplate)
	if cfg.AIEnabled && cfg.AIProvider == "gemini" && cfg.GeminiAPIKey != "" {
		prompt := RenderTemplate(loadTemplateOrDefault("prompts/newcoins.prompts.md", defaultPrompt), renderCtx)
		log.Printf("[Gemini] prompt:\n%s", prompt)
		text, err := callGemini(ctx, client, cfg, prompt)
		if err == nil {
			log.Printf("[Gemini] response:\n%s", text)
			clean := sanitizeAIText(text)
			if clean != "" {
				return clean, nil
			}
		}
	}
	return RenderTemplate(fallback, renderCtx), nil
}

func loadTemplateOrDefault(path string, fallback string) string {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fallback
	}
	return string(b)
}

func callGemini(ctx context.Context, client *http.Client, cfg Config, prompt string) (string, error) {
	u := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", cfg.AIModel)
	payload := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": prompt}}}}}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	req.Header.Set("x-goog-api-key", cfg.GeminiAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini error: %s %s", resp.Status, string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	cands, _ := parsed["candidates"].([]any)
	if len(cands) == 0 {
		return "", nil
	}
	cand, _ := cands[0].(map[string]any)
	content, _ := cand["content"].(map[string]any)
	parts, _ := content["parts"].([]any)
	if len(parts) == 0 {
		return "", nil
	}
	part, _ := parts[0].(map[string]any)
	return strings.TrimSpace(asString(part["text"])), nil
}

func sendTelegramMessage(ctx context.Context, client *http.Client, cfg Config, text string, imageURL string) (*int64, error) {
	formattedText := formatTelegramHTML(text)

	if imageURL != "" {
		if msgID, err := sendTelegramPhoto(ctx, client, cfg, imageURL, text); err == nil {
			return msgID, nil
		}
	}

	return sendTelegramMessageFormatted(ctx, client, cfg, formattedText)
}

func sendTelegramMessageFormatted(ctx context.Context, client *http.Client, cfg Config, formattedText string) (*int64, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramToken)
	payload := telegramSendMessagePayload(cfg.TelegramChannelID, formattedText)
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telegram error: %s %s", resp.Status, string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	ok, _ := parsed["ok"].(bool)
	if !ok {
		return nil, fmt.Errorf("telegram returned non-ok response")
	}
	result, _ := parsed["result"].(map[string]any)
	v := asInt64(result["message_id"])
	if v == 0 {
		return nil, nil
	}
	return &v, nil
}

func sendTelegramPhoto(ctx context.Context, client *http.Client, cfg Config, imageURL, caption string) (*int64, error) {
	formattedCaption := formatTelegramHTML(caption)
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendPhoto", cfg.TelegramToken)
	payload := telegramSendPhotoPayload(cfg.TelegramChannelID, imageURL)
	if len([]rune(formattedCaption)) <= 1024 {
		payload["caption"] = formattedCaption
	}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("telegram photo error: %s %s", resp.Status, string(b))
	}
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	ok, _ := parsed["ok"].(bool)
	if !ok {
		return nil, fmt.Errorf("telegram returned non-ok response for sendPhoto")
	}
	result, _ := parsed["result"].(map[string]any)
	v := asInt64(result["message_id"])
	if v == 0 {
		return nil, nil
	}
	if _, hasCaption := payload["caption"]; !hasCaption {
		return sendTelegramMessageFormatted(ctx, client, cfg, formattedCaption)
	}
	return &v, nil
}

func sanitizeAIText(text string) string {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimPrefix(trimmed, "markdown")
	trimmed = strings.TrimSpace(trimmed)
	return trimmed
}

var markdownBoldRE = regexp.MustCompile(`\*\*([^*]+)\*\*`)

func telegramSendMessagePayload(chatID, formattedText string) map[string]any {
	return map[string]any{"chat_id": chatID, "text": formattedText, "parse_mode": "HTML", "disable_web_page_preview": true}
}

func telegramSendPhotoPayload(chatID, imageURL string) map[string]any {
	return map[string]any{"chat_id": chatID, "photo": imageURL, "parse_mode": "HTML"}
}

func formatTelegramHTML(text string) string {
	escaped := html.EscapeString(strings.TrimSpace(text))
	escaped = markdownBoldRE.ReplaceAllString(escaped, "<b>$1</b>")
	return escaped
}

func firstCoinImageURL(coins []Coin) string {
	for _, c := range coins {
		if strings.TrimSpace(c.ImageURL) != "" {
			return c.ImageURL
		}
	}
	return ""
}

func writeState(ctx context.Context, stateCollection *mongo.Collection, coinsCollection *mongo.Collection, topN int, convert string, coins []Coin) error {
	if err := replaceStateCoins(ctx, coinsCollection, "top", coins); err != nil {
		return err
	}

	ids := make([]int64, 0, len(coins))
	for _, c := range coins {
		ids = append(ids, c.ID)
	}
	_, err := stateCollection.ReplaceOne(ctx, bson.M{"_id": "top"}, stateDoc{ID: "top", UpdatedAt: time.Now().UTC(), TopN: int64(topN), Convert: convert, IDs: ids}, options.Replace().SetUpsert(true))
	return err
}

func replaceStateCoins(ctx context.Context, coinsCollection *mongo.Collection, stateID string, coins []Coin) error {
	now := time.Now().UTC()
	if _, err := coinsCollection.UpdateMany(ctx, bson.M{"state_id": stateID}, bson.M{"$set": bson.M{"is_active": false, "updated_at": now}}); err != nil {
		return err
	}
	docs := buildStateCoinDocs(stateID, coins, now)
	if len(docs) == 0 {
		return nil
	}
	for _, d := range docs {
		_, err := coinsCollection.UpdateOne(
			ctx,
			bson.M{"state_id": stateID, "id": d.ID},
			bson.M{
				"$set": bson.M{
					"name":                d.Name,
					"symbol":              d.Symbol,
					"rank":                d.Rank,
					"market_cap":          d.MarketCap,
					"market_cap_currency": d.MarketCapCurrency,
					"image_url":           d.ImageURL,
					"is_active":           true,
					"updated_at":          now,
				},
				"$setOnInsert": bson.M{"created_at": now},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func buildStateCoinDocs(stateID string, coins []Coin, now time.Time) []stateCoinDoc {
	out := make([]stateCoinDoc, 0, len(coins))
	for _, coin := range coins {
		out = append(out, stateCoinDoc{
			StateID:           stateID,
			ID:                coin.ID,
			Name:              coin.Name,
			Symbol:            coin.Symbol,
			Rank:              coin.Rank,
			MarketCap:         coin.MarketCap,
			MarketCapCurrency: coin.MarketCapCurrency,
			ImageURL:          coin.ImageURL,
			IsActive:          true,
			Updated:           now,
		})
	}
	return out
}

func loadStateCoins(ctx context.Context, coinsCollection *mongo.Collection, stateID string) ([]Coin, error) {
	cur, err := coinsCollection.Find(ctx, bson.M{"state_id": stateID, "is_active": true}, options.Find().SetSort(bson.M{"rank": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)

	out := []Coin{}
	for cur.Next(ctx) {
		var doc stateCoinDoc
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		out = append(out, Coin{ID: doc.ID, Name: doc.Name, Symbol: doc.Symbol, Rank: doc.Rank, MarketCap: doc.MarketCap, MarketCapCurrency: doc.MarketCapCurrency, ImageURL: doc.ImageURL})
	}
	return out, cur.Err()
}

func coinSymbols(coins []Coin) []string {
	symbols := make([]string, 0, len(coins))
	for _, coin := range coins {
		symbols = append(symbols, coin.Symbol)
	}
	return symbols
}

func RenderTemplate(t string, ctx map[string]any) string { return renderBlock(t, ctx, nil) }

func renderBlock(t string, root map[string]any, local map[string]any) string {
	var out strings.Builder
	for i := 0; i < len(t); {
		s := t[i:]
		if strings.HasPrefix(s, "%%") {
			out.WriteByte('%')
			i += 2
			continue
		}
		if strings.HasPrefix(s, "%EACH ") {
			end := strings.Index(t[i+6:], "%")
			if end < 0 {
				break
			}
			tag := strings.TrimSpace(t[i+6 : i+6+end])
			blockStart := i + 6 + end + 1
			endEach := strings.Index(t[blockStart:], "%END_EACH%")
			if endEach < 0 {
				break
			}
			block := t[blockStart : blockStart+endEach]
			if arr := toSlice(resolve(local, root, tag)); len(arr) > 0 {
				for _, it := range arr {
					if m, ok := toMap(it); ok {
						out.WriteString(renderBlock(block, root, m))
					}
				}
			}
			i = blockStart + endEach + len("%END_EACH%")
			continue
		}
		if strings.HasPrefix(s, "%IF ") {
			end := strings.Index(t[i+4:], "%")
			if end < 0 {
				break
			}
			varName := strings.TrimSpace(t[i+4 : i+4+end])
			blockStart := i + 4 + end + 1
			endIf := strings.Index(t[blockStart:], "%END_IF%")
			if endIf < 0 {
				break
			}
			block := t[blockStart : blockStart+endIf]
			if truthy(resolve(local, root, varName)) {
				out.WriteString(renderBlock(block, root, local))
			}
			i = blockStart + endIf + len("%END_IF%")
			continue
		}
		if t[i] == '%' {
			end := strings.Index(t[i+1:], "%")
			if end < 0 {
				out.WriteByte('%')
				i++
				continue
			}
			token := t[i+1 : i+1+end]
			parts := strings.SplitN(token, "|", 2)
			key := strings.TrimSpace(parts[0])
			def := ""
			if len(parts) > 1 {
				def = parts[1]
			}
			val := stringify(resolve(local, root, key))
			if strings.TrimSpace(val) == "" {
				val = def
			}
			out.WriteString(val)
			i += end + 2
			continue
		}
		out.WriteByte(t[i])
		i++
	}
	return out.String()
}

func resolve(local, root map[string]any, key string) any {
	if key == "" {
		return nil
	}
	if local != nil {
		if v, ok := local[key]; ok {
			return v
		}
	}
	return root[key]
}
func truthy(v any) bool {
	switch vv := v.(type) {
	case nil:
		return false
	case string:
		return vv != ""
	default:
		rv := reflect.ValueOf(v)
		if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
			return rv.Len() > 0
		}
		return true
	}
}

func toSlice(v any) []any {
	if v == nil {
		return nil
	}
	if arr, ok := v.([]any); ok {
		return arr
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out
}
func stringify(v any) string {
	switch vv := v.(type) {
	case nil:
		return ""
	case string:
		return vv
	case float64:
		return strconv.FormatFloat(vv, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(vv), 'f', -1, 64)
	case int, int64, int32, uint64:
		return fmt.Sprintf("%v", vv)
	case bool:
		if vv {
			return "true"
		}
		return "false"
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
func asString(v any) string { s, _ := v.(string); return s }
func asStringDef(v any, def string) string {
	if s := asString(v); s != "" {
		return s
	}
	return def
}
func asInt64(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	default:
		return 0
	}
}
func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
func toMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, false
	}
	return out, true
}
