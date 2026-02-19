use coinmarketcap_top100_bot::{run_once, Config, RunOptions};
use lambda_http::{run, service_fn, Body, Error, Request, Response};

#[tokio::main]
async fn main() -> Result<(), Error> {
    run(service_fn(handler)).await
}

async fn handler(_request: Request) -> Result<Response<Body>, Error> {
    let cfg = Config::from_env()?;
    let options = RunOptions {
        dry_run: false,
        notify_exits: false,
        convert: std::env::var("CONVERT").unwrap_or_else(|_| "USD".to_string()),
    };

    run_once(&cfg, &options).await?;

    let resp = Response::builder()
        .status(200)
        .body(Body::Text("ok".to_string()))?;
    Ok(resp)
}
