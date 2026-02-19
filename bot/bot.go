package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	DryRun      bool
	NotifyExits bool
	Convert     string
}

type Config struct {
	CMCAPIKey                string
	TelegramToken            string
	TelegramChannelID        string
	MongoDBConnectionString  string
	MongoDBDatabase          string
	MongoDBStateCollection   string
	MongoDBHistoryCollection string
	TopN                     int
	AIEnabled                bool
	AIProvider               string
	AIModel                  string
	GeminiAPIKey             string
}

func ConfigFromEnv(dryRun bool) (Config, error) {
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
	mongoURI, err := req("MONGODB_CONNECTION_STRING")
	if err != nil {
		return Config{}, err
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
	Coins     []Coin    `bson:"coins"`
	IDs       []int64   `bson:"ids"`
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
	httpClient := &http.Client{Timeout: 30 * time.Second}
	db, client, err := connectDB(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Disconnect(context.Background())

	current, err := fetchCMCTopN(ctx, httpClient, cfg, opt)
	if err != nil {
		return err
	}

	stateColl := db.Collection(cfg.MongoDBStateCollection)
	historyColl := db.Collection(cfg.MongoDBHistoryCollection)

	var prev stateDoc
	err = stateColl.FindOne(ctx, bson.M{"_id": "top"}).Decode(&prev)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return writeState(ctx, stateColl, cfg.TopN, opt.Convert, current)
	}
	if err != nil {
		return err
	}

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
		return nil
	}

	exitedCoins := []Coin{}
	if opt.NotifyExits {
		for _, c := range prev.Coins {
			if _, ok := currentSet[c.ID]; !ok {
				exitedCoins = append(exitedCoins, c)
			}
		}
	}

	recentPosts, err := loadRecentPosts(ctx, historyColl)
	if err != nil {
		return err
	}
	renderCtx := buildRenderContext(cfg, opt, newCoins, exitedCoins, recentPosts)
	text, err := produceTelegramText(ctx, httpClient, cfg, renderCtx)
	if err != nil {
		return err
	}

	if opt.DryRun {
		fmt.Println(text)
		return nil
	}

	msgID, err := sendTelegramMessage(ctx, httpClient, cfg, text)
	if err != nil {
		return err
	}
	if err := writeState(ctx, stateColl, cfg.TopN, opt.Convert, current); err != nil {
		return err
	}
	newIDs := make([]int64, 0, len(newCoins))
	for _, c := range newCoins {
		newIDs = append(newIDs, c.ID)
	}
	_, err = historyColl.InsertOne(ctx, historyDoc{
		CreatedAt: time.Now().UTC(), TopN: int64(cfg.TopN), Convert: opt.Convert,
		NewCoinIDs: newIDs, Text: text, MentionedCoins: newCoins, TelegramMessageID: msgID,
	})
	return err
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
	return coins, nil
}

func loadRecentPosts(ctx context.Context, coll *mongo.Collection) ([]RecentPost, error) {
	cur, err := coll.Find(ctx, bson.M{}, options.Find().SetSort(bson.M{"created_at": -1}).SetLimit(3))
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
		text, err := callGemini(ctx, client, cfg, prompt)
		if err == nil && strings.TrimSpace(text) != "" {
			return text, nil
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

func sendTelegramMessage(ctx context.Context, client *http.Client, cfg Config, text string) (*int64, error) {
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramToken)
	payload := map[string]any{"chat_id": cfg.TelegramChannelID, "text": text, "disable_web_page_preview": true}
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

func writeState(ctx context.Context, coll *mongo.Collection, topN int, convert string, coins []Coin) error {
	ids := make([]int64, 0, len(coins))
	for _, c := range coins {
		ids = append(ids, c.ID)
	}
	_, err := coll.ReplaceOne(ctx, bson.M{"_id": "top"}, stateDoc{ID: "top", UpdatedAt: time.Now().UTC(), TopN: int64(topN), Convert: convert, Coins: coins, IDs: ids}, options.Replace().SetUpsert(true))
	return err
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
			if arr, ok := resolve(local, root, tag).([]any); ok {
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
		return true
	}
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
