//! neurocache-hotpath — Phase-1 standalone Rust binary.
//!
//! This is NOT a replacement for the main Go server (which speaks
//! ~545 commands and ships every AI primitive). It's a focused
//! proof-of-architecture: implement the ~10 bench-critical commands
//! on a single-threaded async I/O loop and measure whether the
//! architecture closes the per-command throughput gap that pure-Go
//! hits structurally.
//!
//! Phase 1 scope:
//!   PING, ECHO, GET, SET, INCR, DECR, INCRBY, DECRBY, DEL, EXISTS
//!
//! Phase 2 (separate sessions): list/hash/set families, pub/sub.
//! Phase 3: link as cgo lib so the Go process can hand off the RESP
//! listener to this loop.
//!
//! Configure via env vars:
//!   NEUROCACHE_HOTPATH_ADDR=127.0.0.1:6380   (default)

mod commands;
mod resp;
mod server;
mod store;

use std::sync::Arc;

fn main() -> std::io::Result<()> {
    let addr = std::env::var("NEUROCACHE_HOTPATH_ADDR")
        .unwrap_or_else(|_| "127.0.0.1:6380".to_string());

    // tokio current_thread flavor: one OS thread runs the entire
    // event loop. This is the architectural difference vs the Go
    // server — no goroutine scheduling between commands, no
    // contended mutex on the hot path. spawn_local needs LocalSet.
    let rt = tokio::runtime::Builder::new_current_thread()
        .enable_io()
        .enable_time()
        .build()?;
    let local = tokio::task::LocalSet::new();
    rt.block_on(local.run_until(async move {
        let store = Arc::new(store::Store::new());
        // Trap SIGINT/SIGTERM so the binary shuts down cleanly when
        // someone Ctrl-C's it during a bench.
        let server_fut = server::run(&addr, store);
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
