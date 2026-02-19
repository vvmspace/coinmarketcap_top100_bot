# CoinMarketCap Top 100 Trends

## Role

You are Senior Rust Cloud Netlify Developer and Architect

## Project
coinmarketcap_top100_bot (Rust CLI)

## Purpose
Run-once CLI: fetch CoinMarketCap Top-N (N from TOP_N env, default 100), detect new entrants vs previous snapshot in MongoDB, then:
- if AI is enabled: send ONE request to AI with (a) all new coins and (b) the last 3 published posts, and the AI returns the final Telegram post text
- if AI is disabled/unavailable/fails: render a fallback post from a template and send it

## Repository conventions

### Prompt templates
- Folder: `prompts/`
- All prompt templates MUST end with `.prompts.md`
- Required file: `prompts/newcoins.prompts.md` (this renders the single AI request prompt that asks the AI to write the whole post)

### Message templates
- Folder: `templates/`
- Required file:
  - `templates/telegram_post_fallback.template.md` (used when AI is disabled/unavailable/fails)

## Templating
Use a simple percent-placeholder syntax.

### Variables
- `%var%` - inserts value or empty string if missing
- `%var|default%` - inserts value or `default` if missing/empty
- default can be omitted: `%var|%` (treat as empty default)
- variable names are snake_case

### Escaping
- `%%` renders a literal `%`

### Loops
- `%EACH items% ... %END_EACH%`

Inside the loop:
- fields resolve from the current item first (eg `name`, `symbol`, `rank`, `id`, `text`)
- if not found, resolve from the global context (eg `top_n`, `convert`, `timestamp_utc`)

### Conditionals
- `%IF var% ... %END_IF%`

Truthy rule:
- missing/null/empty-string -> false
- otherwise -> true

## Runtime loading strategy
- Load templates from disk (repo root).
- If a template/prompt file is missing/unreadable:
  - fallback to embedded defaults via `include_str!`.

## Configuration

### Required env vars
- CMC_API_KEY
- TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN
- TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID
- MONGODB_CONNECTION_STRING

### Optional env vars
- TOP_N=100
- LOG_LEVEL=info
- MONGODB_DB=cmc_top
- MONGODB_STATE_COLLECTION=state
- MONGODB_HISTORY_COLLECTION=history

### AI env vars (optional)
- AI_ENABLED=true|false (default true if GEMINI_API_KEY is present)
- AI_PROVIDER=gemini (only provider in v1)
- AI_MODEL=gemini-3-flash-preview (or gemini-3-pro-preview)
- GEMINI_API_KEY

Gemini docs (Gemini 3 + API): https://ai.google.dev/gemini-api/docs/gemini-3

### CLI flags
- --dry-run
- --notify-exits
- --convert USD (default USD)

## Stable render context contract

Top-level:
- project_name: string (default "coinmarketcap_top100_bot")
- timestamp_utc: string (ISO-8601)
- top_n: number (default 100)
- convert: string (default "USD")
- new_coins: array (default [])
- exited_coins: array (default []) - only used when --notify-exits
- recent_posts: array (default []) - last 3 published posts, most recent first

Coin object:
- id: number (default 0)
- name: string (default "Unknown")
- symbol: string (default "???")
- rank: number (default 0)

Recent post object:
- created_at_utc: string (ISO-8601)
- text: string

## External API usage

### CoinMarketCap
- listings/latest sorted by market cap desc
- `limit = top_n`
- auth header `X-CMC_PRO_API_KEY`

### Telegram
- sendMessage using bot token from `TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN`
- chat_id from `TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID`

### AI provider abstraction
- One request per run (not per coin)
- AI prompt is rendered from `prompts/newcoins.prompts.md` using the full context:
  - new_coins (all)
  - recent_posts (up to 3)

Gemini REST call (generateContent):
- POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
- Headers:
  - x-goog-api-key: $GEMINI_API_KEY
  - Content-Type: application/json
- Body:
  {
    "contents": [{
      "parts": [{"text": "<PROMPT_TEXT>"}]
    }]
  }

## MongoDB model

State doc (upsert by _id="top"):
- _id: "top"
- updated_at
- top_n
- convert
- coins [{id,symbol,name,rank}]
- ids [id]

History collection (append only, written only after Telegram success):
- created_at
- top_n
- convert
- new_coin_ids [id]
- text (exact Telegram text that was sent)
- telegram_message_id (optional, if available)

Recent posts for AI context:
- query history by created_at desc, limit 3, use `text` field

## Core algorithm (updated)

1) Load config, compute `top_n` from TOP_N (default 100, validate >0).
2) Fetch current Top-N from CoinMarketCap.
3) Load previous state from Mongo:
   - If missing: write baseline and exit 0 (no Telegram post).
4) Diff:
   - new = current_ids - prev_ids
   - exited = prev_ids - current_ids only if --notify-exits
5) If `new` is empty: exit 0 (no Telegram post).
6) Load last 3 published posts from Mongo history -> `recent_posts`.
7) Build render context (with defaults).
8) Produce Telegram text:
   - If AI enabled and GEMINI_API_KEY present:
     - render `prompts/newcoins.prompts.md` once (it includes all new coins + recent posts)
     - call AI once
     - use AI output as final Telegram message text
     - If AI output is empty/unusable -> fallback template
   - Else:
     - render `templates/telegram_post_fallback.template.md`
9) If --dry-run: print final message and exit 0.
10) Send Telegram message.
11) Only if Telegram send succeeded:
   - update Mongo state
   - append to history (store the exact text that was sent)

## Failure rules
- Telegram send fails -> DO NOT update state and DO NOT append history.
- AI fails -> use fallback template.
- No panics; clean errors.

## Netlify scheduled run (Rust wrapper, universal)
One Rust codebase that can:
- run locally as CLI
- run on Netlify as a Scheduled Function calling the same `run_once(...)`

Schedule:
- Dubai UTC+4 -> 16:20 Dubai == 12:20 UTC -> cron: `20 12 * * *`
- For Rust (non-JS/TS), schedule is configured in `netlify.toml`.

Rust on Netlify:
- Guide: https://www.netlify.com/blog/2021/10/14/write-netlify-functions-in-rust/
- Example repo: https://github.com/netlify/rust-functions-example

## Repo docs convention (comment)
Create symlinks so tools that expect GEMINI.md or CLAUDE.md still read the same agent rules:
- GEMINI.md -> AGENTS.md
- CLAUDE.md -> AGENTS.md
Example:
- ln -sf AGENTS.md GEMINI.md
- ln -sf AGENTS.md CLAUDE.md
