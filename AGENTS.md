# CoinMarketCap Top 100 Trends

## Role

You are Senior Rust Cloud Netlify Developer and Architect

## Project
coinmarketcap_top100_bot (Rust CLI & Netlify cron)

## Purpose
Run-once CLI: fetch CoinMarketCap Top-N (N from TOP_N env, default 100), detect new entrants vs previous snapshot in MongoDB, render a Telegram post from templates, send it to a Telegram channel. Optionally augment each new coin with short AI notes via an AI-provider abstraction (Gemini first).

## Repository conventions
### Prompt templates
- Folder: `prompts/`
- All prompt templates MUST end with `.prompt.md`
- Required file: `prompts/newcoins.prompt.md`

### Message templates
- Folder: `templates/`
- Required files:
  - `templates/telegram_post.template.md` (primary - can include AI notes)
  - `templates/telegram_post_fallback.template.md` (fallback - no AI notes)

## Template format
Use a simple percent-placeholder syntax.

### Variables
- `%var%` - inserts value or empty string if missing
- `%var|default%` - inserts value or `default` if missing/empty
- default can be omitted: `%var|%` (treat as empty default)
- variable names are snake_case

### Escaping
- `%%` renders a literal `%`

### Loops
- `%EACH new_coins% ... %END_EACH%`
Inside the loop:
- fields resolve from the current item first (eg `name`, `symbol`, `rank`, `id`, `ai_note`)
- if not found, resolve from the global context (eg `top_n`, `convert`, `timestamp_utc`)

### Conditionals
- `%IF var% ... %END_IF%`
Truthy rule:
- missing/null/empty-string -> false
- otherwise -> true

## Runtime loading strategy
- Load templates from disk (repo root).
- If a template/prompt file is missing/unreadable:
  - fallback to embedded defaults compiled via `include_str!`.

## Configuration
### Required env vars
- CMC_API_KEY
- TELEGRAM_BOT_TOKEN
- TELEGRAM_CHAT_ID
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

### CLI flags
- --dry-run
- --notify-exits
- --convert USD (default USD)

## Netlify/Local universal wrapper requirement
Provide ONE universal wrapper entrypoint that:
- can run locally as a normal Node script (for quick testing/manual runs)
- can also be deployed to Netlify as a Scheduled Function without code changes

Wrapper requirements:
- Location: `wrapper/run.mjs`
- Exposes:
  - a CLI entry (when executed with `node wrapper/run.mjs`)
  - a Netlify function handler export (same file)
- Runs the Rust binary via `execFile` (not shell), passing through env vars.
- Locates the binary using:
  - `BOT_BIN` env var if set (explicit path), otherwise
  - `./target/release/coinmarketcap_top100_bot` when local, otherwise
  - `./wrapper/bin/coinmarketcap_top100_bot` when packaged for Netlify (or similar predictable relative path)
- Bundles `prompts/**` and `templates/**` for Netlify runtime (included files).

Schedule:
- Netlify scheduled functions run in UTC.
- Dubai is UTC+4 => 16:20 Dubai == 12:20 UTC => schedule `20 12 * * *`.

## External API usage
### CoinMarketCap
- listings/latest sorted by market cap desc
- auth header `X-CMC_PRO_API_KEY`

### Telegram
- sendMessage

### AI provider abstraction
Gemini REST call (generateContent):
- POST https://generativelanguage.googleapis.com/v1beta/models/{model}:generateContent
- Headers:
  - x-goog-api-key: $GEMINI_API_KEY
  - Content-Type: application/json

## Core algorithm
1) Load config, compute top_n from TOP_N.
2) Fetch current Top-N.
3) Load previous Mongo state (baseline on first run).
4) Diff new entrants.
5) Build context, generate AI notes (optional).
6) Render Telegram text from templates.
7) Send Telegram.
8) Update Mongo only after Telegram succeeded.

## Failure rules
- Telegram send fails => no state update.
- AI fails => fallback template or omit ai_note per coin.
- No panics; clean errors.

## Security
- Never log secrets.
- AI output must be neutral; no financial advice.

## Repo docs convention (comment)
Create symlinks so tools that expect GEMINI.md or CLAUDE.md still read the same agent rules:
- GEMINI.md -> AGENTS.md
- CLAUDE.md -> AGENTS.md
Example:
- ln -sf AGENTS.md GEMINI.md
- ln -sf AGENTS.md CLAUDE.md
