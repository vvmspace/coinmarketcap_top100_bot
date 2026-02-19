use anyhow::Result;
use clap::Parser;
use coinmarketcap_top100_bot::{run_once, Config, RunOptions};

#[derive(Debug, Parser)]
#[command(author, version, about)]
struct Cli {
    #[arg(long)]
    dry_run: bool,
    #[arg(long)]
    notify_exits: bool,
    #[arg(long, default_value = "USD")]
    convert: String,
}

#[tokio::main]
async fn main() -> Result<()> {
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("info")).init();

    let cli = Cli::parse();
    let cfg = if cli.dry_run {
        Config::from_env_for_dry_run()?
    } else {
        Config::from_env()?
    };
    let opts = RunOptions {
        dry_run: cli.dry_run,
        notify_exits: cli.notify_exits,
        convert: cli.convert,
    };
    run_once(&cfg, &opts).await
}
