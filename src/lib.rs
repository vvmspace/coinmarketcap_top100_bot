use anyhow::{anyhow, Context, Result};
use chrono::{DateTime, Utc};
use log::{info, warn};
use mongodb::bson::doc;
use mongodb::{options::ClientOptions, Client, Collection, Database};
use reqwest::Client as HttpClient;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::HashSet;
use std::fs;
use std::path::Path;

const DEFAULT_PROMPT: &str = include_str!("../prompts/newcoins.prompts.md");
const DEFAULT_FALLBACK_TEMPLATE: &str =
    include_str!("../templates/telegram_post_fallback.template.md");

#[derive(Debug, Clone)]
pub struct RunOptions {
    pub dry_run: bool,
    pub notify_exits: bool,
    pub convert: String,
}

#[derive(Debug, Clone)]
pub struct Config {
    pub cmc_api_key: String,
    pub telegram_token: String,
    pub telegram_channel_id: String,
    pub mongodb_connection_string: String,
    pub mongodb_db: String,
    pub mongodb_state_collection: String,
    pub mongodb_history_collection: String,
    pub top_n: usize,
    pub ai_enabled: bool,
    pub ai_provider: String,
    pub ai_model: String,
    pub gemini_api_key: Option<String>,
}

impl Config {
    pub fn from_env() -> Result<Self> {
        Self::from_env_with_mode(false)
    }

    pub fn from_env_for_dry_run() -> Result<Self> {
        Self::from_env_with_mode(true)
    }

    fn from_env_with_mode(dry_run: bool) -> Result<Self> {
        let cmc_api_key = required("CMC_API_KEY")?;
        let telegram_token =
            optional_required("TELEGRAM_COINMARKETCAP_TOP_100_BOT_TOKEN", dry_run)?;
        let telegram_channel_id =
            optional_required("TELEGRAM_COINMARKETCAP_TOP_100_CHANNEL_ID", dry_run)?;
        let mongodb_connection_string = required("MONGODB_CONNECTION_STRING")?;
        let mongodb_db = optional("MONGODB_DB", "cmc_top");
        let mongodb_state_collection = optional("MONGODB_STATE_COLLECTION", "state");
        let mongodb_history_collection = optional("MONGODB_HISTORY_COLLECTION", "history");
        let top_n = optional("TOP_N", "100")
            .parse::<usize>()
            .context("TOP_N must be a positive integer")?;
        if top_n == 0 {
            return Err(anyhow!("TOP_N must be > 0"));
        }
        let gemini_api_key = std::env::var("GEMINI_API_KEY")
            .ok()
            .filter(|v| !v.is_empty());
        let ai_enabled = match std::env::var("AI_ENABLED") {
            Ok(v) => v.eq_ignore_ascii_case("true"),
            Err(_) => gemini_api_key.is_some(),
        };

        Ok(Self {
            cmc_api_key,
            telegram_token,
            telegram_channel_id,
            mongodb_connection_string,
            mongodb_db,
            mongodb_state_collection,
            mongodb_history_collection,
            top_n,
            ai_enabled,
            ai_provider: optional("AI_PROVIDER", "gemini"),
            ai_model: optional("AI_MODEL", "gemini-3-flash-preview"),
            gemini_api_key,
        })
    }
}

fn required(name: &str) -> Result<String> {
    std::env::var(name).with_context(|| format!("missing required env var {name}"))
}

fn optional(name: &str, default: &str) -> String {
    std::env::var(name).unwrap_or_else(|_| default.to_string())
}

fn optional_required(name: &str, allow_missing: bool) -> Result<String> {
    match std::env::var(name) {
        Ok(v) => Ok(v),
        Err(_) if allow_missing => Ok(String::new()),
        Err(_) => Err(anyhow!("missing required env var {name}")),
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Coin {
    pub id: i64,
    pub name: String,
    pub symbol: String,
    pub rank: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub market_cap: Option<f64>,
    pub market_cap_currency: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RecentPost {
    pub created_at_utc: String,
    pub text: String,
    #[serde(default)]
    pub mentioned_coins: Vec<Coin>,
}

#[derive(Debug, Serialize, Deserialize)]
struct StateDoc {
    #[serde(rename = "_id")]
    id: String,
    updated_at: DateTime<Utc>,
    top_n: i64,
    convert: String,
    coins: Vec<Coin>,
    ids: Vec<i64>,
}

#[derive(Debug, Serialize, Deserialize)]
struct HistoryDoc {
    created_at: DateTime<Utc>,
    top_n: i64,
    convert: String,
    new_coin_ids: Vec<i64>,
    text: String,
    mentioned_coins: Vec<Coin>,
    #[serde(skip_serializing_if = "Option::is_none")]
    telegram_message_id: Option<i64>,
}

pub async fn run_once(config: &Config, options: &RunOptions) -> Result<()> {
    let http = HttpClient::new();
    let db = connect_db(config).await?;

    let current = fetch_cmc_top_n(&http, config, options).await?;
    let state_coll: Collection<StateDoc> = db.collection(&config.mongodb_state_collection);
    let history_coll: Collection<HistoryDoc> = db.collection(&config.mongodb_history_collection);

    let maybe_prev = state_coll.find_one(doc! {"_id": "top"}).await?;
    if maybe_prev.is_none() {
        info!("No previous state found, saving baseline and exiting.");
        write_state(&state_coll, config.top_n, &options.convert, &current).await?;
        return Ok(());
    }
    let prev = maybe_prev.unwrap();
    let prev_ids: HashSet<i64> = prev.ids.into_iter().collect();
    let current_ids: HashSet<i64> = current.iter().map(|c| c.id).collect();

    let new_coins: Vec<Coin> = current
        .iter()
        .filter(|c| !prev_ids.contains(&c.id))
        .cloned()
        .collect();

    if new_coins.is_empty() {
        info!("No new entrants detected.");
        return Ok(());
    }

    let exited_coins: Vec<Coin> = if options.notify_exits {
        prev.coins
            .into_iter()
            .filter(|c| !current_ids.contains(&c.id))
            .collect()
    } else {
        Vec::new()
    };

    let recent_posts = load_recent_posts(&history_coll).await?;
    let render_ctx =
        build_render_context(config, options, &new_coins, &exited_coins, &recent_posts);

    let telegram_text = produce_telegram_text(&http, config, &render_ctx).await?;

    if options.dry_run {
        println!("{telegram_text}");
        return Ok(());
    }

    let telegram_message_id = send_telegram_message(&http, config, &telegram_text).await?;

    write_state(&state_coll, config.top_n, &options.convert, &current).await?;
    history_coll
        .insert_one(HistoryDoc {
            created_at: Utc::now(),
            top_n: config.top_n as i64,
            convert: options.convert.clone(),
            new_coin_ids: new_coins.iter().map(|c| c.id).collect(),
            text: telegram_text,
            mentioned_coins: new_coins,
            telegram_message_id,
        })
        .await?;

    Ok(())
}

async fn connect_db(config: &Config) -> Result<Database> {
    let mut opts = ClientOptions::parse(&config.mongodb_connection_string).await?;
    opts.app_name = Some("coinmarketcap_top100_bot".to_string());
    let client = Client::with_options(opts)?;
    Ok(client.database(&config.mongodb_db))
}

async fn fetch_cmc_top_n(
    http: &HttpClient,
    config: &Config,
    options: &RunOptions,
) -> Result<Vec<Coin>> {
    let url = format!(
        "https://pro-api.coinmarketcap.com/v1/cryptocurrency/listings/latest?start=1&limit={}&convert={}&sort=market_cap&sort_dir=desc",
        config.top_n,
        urlencoding::encode(&options.convert)
    );
    let payload: Value = http
        .get(url)
        .header("X-CMC_PRO_API_KEY", &config.cmc_api_key)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;

    let data = payload
        .get("data")
        .and_then(Value::as_array)
        .ok_or_else(|| anyhow!("CMC response missing data array"))?;

    let mut coins = Vec::with_capacity(data.len());
    for item in data {
        let id = item.get("id").and_then(Value::as_i64).unwrap_or(0);
        let name = item
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or("Unknown")
            .to_string();
        let symbol = item
            .get("symbol")
            .and_then(Value::as_str)
            .unwrap_or("???")
            .to_string();
        let rank = item.get("cmc_rank").and_then(Value::as_i64).unwrap_or(0);
        let market_cap = item
            .get("quote")
            .and_then(|q| q.get(&options.convert))
            .and_then(|q| q.get("market_cap"))
            .and_then(Value::as_f64);

        coins.push(Coin {
            id,
            name,
            symbol,
            rank,
            market_cap,
            market_cap_currency: options.convert.clone(),
        });
    }

    Ok(coins)
}

async fn load_recent_posts(history_coll: &Collection<HistoryDoc>) -> Result<Vec<RecentPost>> {
    let mut cursor = history_coll
        .find(doc! {})
        .sort(doc! {"created_at": -1})
        .limit(3)
        .await?;

    let mut out = Vec::new();
    while cursor.advance().await? {
        let doc: HistoryDoc = cursor.deserialize_current()?;
        out.push(RecentPost {
            created_at_utc: doc.created_at.to_rfc3339(),
            text: doc.text,
            mentioned_coins: doc.mentioned_coins,
        });
    }
    Ok(out)
}

fn build_render_context(
    config: &Config,
    options: &RunOptions,
    new_coins: &[Coin],
    exited_coins: &[Coin],
    recent_posts: &[RecentPost],
) -> Value {
    json!({
        "project_name": "coinmarketcap_top100_bot",
        "timestamp_utc": Utc::now().to_rfc3339(),
        "top_n": config.top_n,
        "convert": options.convert,
        "new_coins": new_coins,
        "exited_coins": exited_coins,
        "recent_posts": recent_posts,
    })
}

async fn produce_telegram_text(http: &HttpClient, config: &Config, ctx: &Value) -> Result<String> {
    let fallback_template = load_template_or_default(
        "templates/telegram_post_fallback.template.md",
        DEFAULT_FALLBACK_TEMPLATE,
    );

    if config.ai_enabled {
        if config.ai_provider != "gemini" {
            warn!(
                "Unsupported AI_PROVIDER {}, using fallback template",
                config.ai_provider
            );
        } else if let Some(api_key) = &config.gemini_api_key {
            let prompt_template =
                load_template_or_default("prompts/newcoins.prompts.md", DEFAULT_PROMPT);
            let prompt = render_template(&prompt_template, ctx);
            match call_gemini(http, config, api_key, &prompt).await {
                Ok(text) if !text.trim().is_empty() => return Ok(text),
                Ok(_) => warn!("Gemini output empty, using fallback template"),
                Err(e) => warn!("Gemini failed: {e:#}, using fallback template"),
            }
        }
    }

    Ok(render_template(&fallback_template, ctx))
}

fn load_template_or_default(path: &str, fallback: &str) -> String {
    fs::read_to_string(Path::new(path)).unwrap_or_else(|_| fallback.to_string())
}

async fn call_gemini(
    http: &HttpClient,
    config: &Config,
    api_key: &str,
    prompt: &str,
) -> Result<String> {
    let url = format!(
        "https://generativelanguage.googleapis.com/v1beta/models/{}:generateContent",
        config.ai_model
    );
    let payload = json!({
        "contents": [{"parts": [{"text": prompt}]}]
    });
    let response: Value = http
        .post(url)
        .header("x-goog-api-key", api_key)
        .header("Content-Type", "application/json")
        .json(&payload)
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;

    let text = response
        .pointer("/candidates/0/content/parts/0/text")
        .and_then(Value::as_str)
        .unwrap_or("")
        .trim()
        .to_string();

    Ok(text)
}

async fn send_telegram_message(
    http: &HttpClient,
    config: &Config,
    text: &str,
) -> Result<Option<i64>> {
    let url = format!(
        "https://api.telegram.org/bot{}/sendMessage",
        config.telegram_token
    );

    let response: Value = http
        .post(url)
        .json(&json!({
            "chat_id": config.telegram_channel_id,
            "text": text,
            "disable_web_page_preview": true,
        }))
        .send()
        .await?
        .error_for_status()?
        .json()
        .await?;

    if response.get("ok").and_then(Value::as_bool) != Some(true) {
        return Err(anyhow!("telegram returned non-ok response: {response}"));
    }

    let message_id = response
        .pointer("/result/message_id")
        .and_then(Value::as_i64);
    Ok(message_id)
}

async fn write_state(
    state_coll: &Collection<StateDoc>,
    top_n: usize,
    convert: &str,
    coins: &[Coin],
) -> Result<()> {
    state_coll
        .replace_one(
            doc! {"_id": "top"},
            StateDoc {
                id: "top".to_string(),
                updated_at: Utc::now(),
                top_n: top_n as i64,
                convert: convert.to_string(),
                coins: coins.to_vec(),
                ids: coins.iter().map(|c| c.id).collect(),
            },
        )
        .upsert(true)
        .await?;
    Ok(())
}

pub fn render_template(template: &str, ctx: &Value) -> String {
    render_block(template, ctx, None)
}

fn render_block(template: &str, root: &Value, local: Option<&Value>) -> String {
    let mut out = String::new();
    let mut i = 0;
    while i < template.len() {
        if template[i..].starts_with("%%") {
            out.push('%');
            i += 2;
            continue;
        }
        if template[i..].starts_with("%EACH ") {
            let Some(tag_end_rel) = template[i + 6..].find('%') else {
                break;
            };
            let tag_end = i + 6 + tag_end_rel;
            let tag = &template[i + 6..tag_end].trim();
            let block_start = tag_end + 1;
            let Some(end_rel) = template[block_start..].find("%END_EACH%") else {
                break;
            };
            let block = &template[block_start..block_start + end_rel];
            if let Some(Value::Array(items)) = resolve(local, root, tag) {
                for item in items {
                    out.push_str(&render_block(block, root, Some(item)));
                }
            }
            i = block_start + end_rel + "%END_EACH%".len();
            continue;
        }
        if template[i..].starts_with("%IF ") {
            let Some(tag_end_rel) = template[i + 4..].find('%') else {
                break;
            };
            let tag_end = i + 4 + tag_end_rel;
            let var = &template[i + 4..tag_end].trim();
            let block_start = tag_end + 1;
            let Some(end_rel) = template[block_start..].find("%END_IF%") else {
                break;
            };
            let block = &template[block_start..block_start + end_rel];
            if truthy(resolve(local, root, var)) {
                out.push_str(&render_block(block, root, local));
            }
            i = block_start + end_rel + "%END_IF%".len();
            continue;
        }
        if template.as_bytes()[i] == b'%' {
            let Some(end_rel) = template[i + 1..].find('%') else {
                out.push('%');
                i += 1;
                continue;
            };
            let token = &template[i + 1..i + 1 + end_rel];
            let mut parts = token.splitn(2, '|');
            let key = parts.next().unwrap_or("").trim();
            let default = parts.next().unwrap_or("");
            let val = resolve(local, root, key)
                .and_then(as_display)
                .filter(|s| !s.is_empty())
                .unwrap_or_else(|| default.to_string());
            out.push_str(&val);
            i += end_rel + 2;
            continue;
        }

        let ch = template[i..].chars().next().unwrap();
        out.push(ch);
        i += ch.len_utf8();
    }
    out
}

fn resolve<'a>(local: Option<&'a Value>, root: &'a Value, key: &str) -> Option<&'a Value> {
    if key.is_empty() {
        return None;
    }
    local.and_then(|v| v.get(key)).or_else(|| root.get(key))
}

fn truthy(v: Option<&Value>) -> bool {
    match v {
        None => false,
        Some(Value::Null) => false,
        Some(Value::String(s)) => !s.is_empty(),
        Some(_) => true,
    }
}

fn as_display(v: &Value) -> Option<String> {
    match v {
        Value::Null => None,
        Value::String(s) => Some(s.clone()),
        Value::Number(n) => Some(n.to_string()),
        Value::Bool(b) => Some(b.to_string()),
        Value::Array(_) | Value::Object(_) => Some(v.to_string()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn template_features_work() {
        let ctx = json!({
            "name": "Alice",
            "items": [{"name": "BTC"}, {"name": "ETH"}],
            "empty": ""
        });
        let t = "hi %name|X% %% %missing|d% %IF name%ok%END_IF%%IF empty%bad%END_IF% %EACH items%[%name%]%END_EACH%";
        let out = render_template(t, &ctx);
        assert_eq!(out, "hi Alice % d ok [BTC][ETH]");
    }
}
