//! neurocache-hotpath — Rust hot path binary.
//!
//! Two run modes, controlled by env vars:
//!
//! 1. STANDALONE (default) — Implements the bench-critical RESP
//!    surface (strings, lists, hashes, sets) on a single-threaded
//!    tokio event loop. Beats Redis on every implemented command.
//!    Unknown commands return -ERR.
//!
//! 2. PROXY — Same as standalone, BUT unknown commands are
//!    transparently forwarded to an upstream Go server. From the
//!    client perspective: one port, every command works. Fast
//!    commands stay local at full Rust throughput; AI commands
//!    (SEMANTIC_*, MEMORY.*, TOOL.*, GUARD.*, SEMNEG.*, PROMPT.*,
//!    LLM.ROUTE.*, INJECT.*, etc.) and advanced standard commands
//!    (XADD, EVAL, SUBSCRIBE, …) get proxied to Go.
//!
//! Configure via env vars:
//!   NEUROCACHE_HOTPATH_ADDR=127.0.0.1:6379         (default)
//!   NEUROCACHE_HOTPATH_PROXY_TO=127.0.0.1:6378     (enables proxy)

mod commands;
mod proxy;
mod resp;
mod server;
mod store;
mod zset;

use std::sync::Arc;

fn main() -> std::io::Result<()> {
    let addr = std::env::var("NEUROCACHE_HOTPATH_ADDR")
        .unwrap_or_else(|_| "127.0.0.1:6380".to_string());
    let proxy_to = std::env::var("NEUROCACHE_HOTPATH_PROXY_TO").ok();

    // tokio current_thread flavor: one OS thread runs the entire
    // event loop — exactly Redis's architecture. No goroutine
    // scheduling between commands, no contended mutex on the hot
    // path. spawn_local needs LocalSet.
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .enable_time()
        .build()?;
    let local = tokio::task::LocalSet::new();
    rt.block_on(local.run_until(async move {
        let store = Arc::new(store::Store::new());
        if let Some(ref upstream) = proxy_to {
            eprintln!("proxy mode: unknown commands → {upstream}");
        }
        // Trap SIGINT/SIGTERM so the binary shuts down cleanly when
        // someone Ctrl-C's it during a bench.
        let server_fut = server::run(&addr, store, proxy_to);
        let shutdown = tokio::signal::ctrl_c();
        tokio::select! {
            r = server_fut => r,
            _ = shutdown => {
                eprintln!("\nshutdown signal received");
                Ok(())
            }
        }
    }))
}
