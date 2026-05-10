//! TCP server + per-connection dispatch loop.
//!
//! Architecture: tokio current_thread runtime — one OS thread runs
//! the entire event loop + every connection's read/write tasks. This
//! is exactly Redis's model. The single thread eliminates the
//! mutex-CAS cost the goroutine-per-connection Go server pays on
//! every command.
//!
//! Per-connection: read into a 64 KiB BytesMut, call the parser
//! repeatedly until it says "incomplete" (pipelined commands are
//! processed back-to-back without yielding), dispatch each, write
//! reply to the socket via a 64 KiB write buffer that flushes when
//! the read side has nothing pending.
//!
//! Phase 3 / proxy mode: when `proxy_to` is set, unknown commands
//! are forwarded to that upstream address. Each conn lazily opens
//! its own upstream conn on first need; fast commands stay 100%
//! local.

use crate::commands;
use crate::proxy::UpstreamConn;
use crate::resp::{parse, Command};
use crate::store::Store;
use bytes::BytesMut;
use std::io;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt, BufWriter};
use tokio::net::{TcpListener, TcpStream};

/// Run the server on `addr` until cancelled. The Store is shared
/// across every connection via Arc — cheap clone, all the actual
/// data lives behind the shard mutexes. `proxy_to` enables proxy
/// mode (forwards unknown commands to the upstream Go server).
pub async fn run(
    addr: &str,
    store: Arc<Store>,
    proxy_to: Option<String>,
) -> io::Result<()> {
    let listener = TcpListener::bind(addr).await?;
    eprintln!("neurocache-hotpath listening on {addr}");
    loop {
        let (sock, _peer) = listener.accept().await?;
        // TCP_NODELAY mirrors what the Go server sets — pipelined
        // bursts shouldn't sit in Nagle's window.
        let _ = sock.set_nodelay(true);
        let store = store.clone();
        let proxy_to = proxy_to.clone();
        tokio::task::spawn_local(async move {
            if let Err(e) = handle(sock, store, proxy_to).await {
                if e.kind() != io::ErrorKind::UnexpectedEof
                    && e.kind() != io::ErrorKind::ConnectionReset
                {
                    eprintln!("conn error: {e}");
                }
            }
        });
    }
}

/// Per-connection driver: read → parse → dispatch → write loop.
async fn handle(
    sock: TcpStream,
    store: Arc<Store>,
    proxy_to: Option<String>,
) -> io::Result<()> {
    let (mut rd, wr) = sock.into_split();
    // 64 KiB write buffer — same as the Go side. Big enough that 16
    // pipelined replies coalesce into one write syscall.
    let mut wr = BufWriter::with_capacity(64 * 1024, wr);
    // 64 KiB read buffer. We append into it; the parser splits frames
    // off when complete.
    let mut buf = BytesMut::with_capacity(64 * 1024);

    // Per-conn lazy upstream connection. Created on first proxied
    // command. Apps that only use fast-path commands never open an
    // upstream conn — zero cost.
    let mut upstream: Option<UpstreamConn> = proxy_to.map(UpstreamConn::new);

    loop {
        // Read one chunk. read_buf appends to `buf` without growing
        // beyond capacity unless needed. Returns 0 on EOF.
        let n = rd.read_buf(&mut buf).await?;
        if n == 0 {
            return Ok(());
        }
        // Parse + dispatch every complete frame in the buffer.
        // Pipelined burst: one read syscall feeds many commands.
        loop {
            match parse(&mut buf)? {
                Some(cmd) => dispatch(&cmd, &store, upstream.as_mut(), &mut wr).await?,
                None => break, // need more bytes
            }
        }
        // Flush only when there's nothing more to parse — same
        // pipelining shape as the Go side. bufio auto-flushes when
        // the 64 KiB window fills.
        wr.flush().await?;
    }
}

/// Dispatch one command. Routes to the per-command handler based on
/// the (uppercased) command name. Unknown commands either return
/// -ERR (standalone mode) or are forwarded to the upstream Go server
/// (proxy mode — when `upstream.is_some()`).
async fn dispatch<W: AsyncWriteExt + Unpin>(
    cmd: &Command,
    store: &Store,
    upstream: Option<&mut UpstreamConn>,
    w: &mut W,
) -> io::Result<()> {
    if cmd.argv.is_empty() {
        return Ok(());
    }
    // Uppercase the command name. We accept either case from the
    // wire (every client library sends upper, redis-cli sends lower).
    let name = crate::resp::uppercase_ascii(&cmd.argv[0]);
    match name.as_slice() {
        // ── connection / server ──
        b"PING" => commands::ping(&cmd.argv, w).await,
        b"ECHO" => commands::echo(&cmd.argv, w).await,
        // ── strings ──
        b"GET" => commands::get(&cmd.argv, store, w).await,
        b"SET" => commands::set(&cmd.argv, store, w).await,
        b"INCR" => commands::incr_by(&cmd.argv, store, w, 1).await,
        b"DECR" => commands::incr_by(&cmd.argv, store, w, -1).await,
        b"INCRBY" => commands::incrby(&cmd.argv, store, w).await,
        b"DECRBY" => commands::decrby(&cmd.argv, store, w).await,
        b"DEL" => commands::del(&cmd.argv, store, w).await,
        b"EXISTS" => commands::exists(&cmd.argv, store, w).await,
        b"MSET" => commands::mset(&cmd.argv, store, w).await,
        b"MGET" => commands::mget(&cmd.argv, store, w).await,
        // ── lists ──
        b"LPUSH" => commands::lpush(&cmd.argv, store, w).await,
        b"RPUSH" => commands::rpush(&cmd.argv, store, w).await,
        b"LPOP" => commands::lpop(&cmd.argv, store, w).await,
        b"RPOP" => commands::rpop(&cmd.argv, store, w).await,
        b"LLEN" => commands::llen(&cmd.argv, store, w).await,
        b"LRANGE" => commands::lrange(&cmd.argv, store, w).await,
        b"LINDEX" => commands::lindex(&cmd.argv, store, w).await,
        // ── hashes ──
        b"HSET" => commands::hset(&cmd.argv, store, w).await,
        b"HGET" => commands::hget(&cmd.argv, store, w).await,
        b"HDEL" => commands::hdel(&cmd.argv, store, w).await,
        b"HLEN" => commands::hlen(&cmd.argv, store, w).await,
        b"HEXISTS" => commands::hexists(&cmd.argv, store, w).await,
        b"HGETALL" => commands::hgetall(&cmd.argv, store, w).await,
        b"HKEYS" => commands::hkeys(&cmd.argv, store, w).await,
        b"HVALS" => commands::hvals(&cmd.argv, store, w).await,
        // ── sets ──
        b"SADD" => commands::sadd(&cmd.argv, store, w).await,
        b"SREM" => commands::srem(&cmd.argv, store, w).await,
        b"SISMEMBER" => commands::sismember(&cmd.argv, store, w).await,
        b"SCARD" => commands::scard(&cmd.argv, store, w).await,
        b"SMEMBERS" => commands::smembers(&cmd.argv, store, w).await,
        b"SPOP" => commands::spop(&cmd.argv, store, w).await,
        // ── client compatibility ──
        b"COMMAND" => w.write_all(b"*0\r\n").await,
        b"HELLO" => {
            w.write_all(
                b"*7\r\n$6\r\nserver\r\n$10\r\nneurocache\r\n$5\r\nproto\r\n:2\r\n$2\r\nid\r\n:0\r\n$4\r\nmode\r\n",
            )
            .await?;
            w.write_all(b"$10\r\nstandalone\r\n").await
        }
        b"QUIT" => {
            w.write_all(b"+OK\r\n").await?;
            Err(io::Error::new(io::ErrorKind::ConnectionReset, "quit"))
        }
        _ => {
            // Unknown command. In proxy mode, forward to the Go
            // upstream — this is how AI commands (SEMANTIC_GET,
            // MEMORY.QUERY, TOOL.GET, GUARD.CHECK, etc.) and
            // advanced standard commands (XADD, EVAL, SUBSCRIBE)
            // reach a user that talks to a single port.
            if let Some(u) = upstream {
                u.forward(cmd, w).await
            } else {
                // Standalone mode — the Phase-2 binary covers the
                // bench-critical surface. Real apps should run with
                // PROXY_TO set so unknown commands get serviced.
                let msg = format!(
                    "-ERR unknown command '{}' (start with NEUROCACHE_HOTPATH_PROXY_TO=… to forward to the Go server)\r\n",
                    String::from_utf8_lossy(&cmd.argv[0])
                );
                w.write_all(msg.as_bytes()).await
            }
        }
    }
}
