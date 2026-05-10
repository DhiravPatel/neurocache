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
///
/// Proxy pipelining: a pipelined batch of N proxied commands gets
/// forwarded with N back-to-back writes; we drain all N replies
/// after the batch parse loop ends. This matches Redis-benchmark's
/// pipelining shape — a single XADD pipeline sends 16 XADDs and
/// expects 16 replies. Without this, the proxy would serialize
/// each as one round-trip and XADD throughput collapses to ~5k/sec.
///
/// Reply ordering: when a local command appears mid-batch after
/// pending proxy commands, we drain pending proxy replies BEFORE
/// dispatching the local command. This keeps reply order matching
/// request order on the wire (RESP requirement).
async fn handle(
    sock: TcpStream,
    store: Arc<Store>,
    proxy_to: Option<String>,
) -> io::Result<()> {
    let (mut rd, wr) = sock.into_split();
    // 64 KiB write buffer — big enough that 16 pipelined replies
    // coalesce into one write syscall.
    let mut wr = BufWriter::with_capacity(64 * 1024, wr);
    let mut buf = BytesMut::with_capacity(64 * 1024);

    // Per-conn lazy upstream connection. Created on first proxied
    // command. Apps that only use fast-path commands never open an
    // upstream conn — zero cost.
    let mut upstream: Option<UpstreamConn> = proxy_to.map(UpstreamConn::new);
    let mut pending_proxy: usize = 0;

    loop {
        let n = rd.read_buf(&mut buf).await?;
        if n == 0 {
            return Ok(());
        }
        // Parse + dispatch every complete frame in the buffer.
        loop {
            match parse(&mut buf)? {
                Some(cmd) => {
                    // Classify FIRST so we can pipeline proxy sends
                    // without waiting per command.
                    if cmd.argv.is_empty() {
                        continue;
                    }
                    let name = crate::resp::uppercase_ascii(&cmd.argv[0]);
                    if is_local(&name) {
                        // Local commands write replies directly to wr.
                        // Drain any pending proxy replies first so
                        // wire order matches request order.
                        if pending_proxy > 0 {
                            if let Some(u) = upstream.as_mut() {
                                u.flush_send().await?;
                                for _ in 0..pending_proxy {
                                    u.recv_one(&mut wr).await?;
                                }
                            }
                            pending_proxy = 0;
                        }
                        dispatch_local(&cmd, &name, &store, &mut wr).await?;
                    } else if let Some(u) = upstream.as_mut() {
                        // Proxy mode: serialize into the upstream
                        // outbox without flushing yet — let the
                        // outbox accumulate the whole pipelined
                        // batch into one write syscall.
                        u.send_only(&cmd).await?;
                        pending_proxy += 1;
                    } else {
                        // Standalone mode + unknown command.
                        let msg = format!(
                            "-ERR unknown command '{}' (start with NEUROCACHE_HOTPATH_PROXY_TO=… to forward to the Go server)\r\n",
                            String::from_utf8_lossy(&cmd.argv[0])
                        );
                        wr.write_all(msg.as_bytes()).await?;
                    }
                }
                None => break, // need more bytes
            }
        }
        // Drain any remaining pending proxy replies. flush_send
        // pushes the accumulated outbox to upstream in ONE write
        // syscall — that's the pipelining win for proxied commands.
        if pending_proxy > 0 {
            if let Some(u) = upstream.as_mut() {
                u.flush_send().await?;
                for _ in 0..pending_proxy {
                    u.recv_one(&mut wr).await?;
                }
            }
            pending_proxy = 0;
        }
        wr.flush().await?;
    }
}

/// is_local returns true for every command the Rust hot path
/// handles itself. Anything else gets proxied (or, in standalone
/// mode, returns -ERR). Kept as a tight match so the dispatcher
/// can branch in O(1).
fn is_local(name: &[u8]) -> bool {
    matches!(
        name,
        // connection / server
        b"PING" | b"ECHO" | b"COMMAND" | b"HELLO" | b"QUIT"
        // strings
        | b"GET" | b"SET" | b"INCR" | b"DECR" | b"INCRBY" | b"DECRBY"
        | b"DEL" | b"EXISTS" | b"MSET" | b"MGET"
        // lists
        | b"LPUSH" | b"RPUSH" | b"LPOP" | b"RPOP"
        | b"LLEN" | b"LRANGE" | b"LINDEX"
        // hashes
        | b"HSET" | b"HGET" | b"HDEL" | b"HLEN" | b"HEXISTS"
        | b"HGETALL" | b"HKEYS" | b"HVALS"
        // sets
        | b"SADD" | b"SREM" | b"SISMEMBER" | b"SCARD" | b"SMEMBERS" | b"SPOP"
        // sorted sets
        | b"ZADD" | b"ZSCORE" | b"ZCARD" | b"ZINCRBY" | b"ZRANGE"
        | b"ZREM" | b"ZPOPMIN" | b"ZPOPMAX"
        // streams (basic XADD + XLEN — full XREAD with consumer
        // groups is Phase 4)
        | b"XADD" | b"XLEN"
        // string extras (Phase 4)
        | b"SETNX" | b"GETSET" | b"GETDEL" | b"STRLEN" | b"APPEND"
        | b"GETRANGE" | b"SUBSTR" | b"BITCOUNT"
        // hash extras
        | b"HMGET" | b"HMSET" | b"HINCRBY" | b"HSETNX"
        // zset extras
        | b"ZRANGEBYSCORE" | b"ZRANK" | b"ZREVRANK"
        // list extras
        | b"LSET" | b"LREM" | b"LTRIM" | b"LINSERT"
        // set extras
        | b"SRANDMEMBER" | b"SMOVE"
        // server / TTL
        | b"TYPE" | b"TTL" | b"PTTL" | b"EXPIRE" | b"PEXPIRE"
        | b"PERSIST" | b"DBSIZE" | b"RANDOMKEY"
    )
}

/// dispatch_local routes a known-local command to its handler.
/// `name` is the already-uppercased command name (caller has
/// is_local-checked it). Proxied commands go through send_only +
/// recv_one in the handle() loop instead of this function.
async fn dispatch_local<W: AsyncWriteExt + Unpin>(
    cmd: &Command,
    name: &[u8],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    match name {
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
        // ── sorted sets ──
        b"ZADD" => commands::zadd(&cmd.argv, store, w).await,
        b"ZSCORE" => commands::zscore(&cmd.argv, store, w).await,
        b"ZCARD" => commands::zcard(&cmd.argv, store, w).await,
        b"ZINCRBY" => commands::zincrby(&cmd.argv, store, w).await,
        b"ZRANGE" => commands::zrange(&cmd.argv, store, w).await,
        b"ZREM" => commands::zrem(&cmd.argv, store, w).await,
        b"ZPOPMIN" => commands::zpopmin(&cmd.argv, store, w).await,
        b"ZPOPMAX" => commands::zpopmax(&cmd.argv, store, w).await,
        // ── streams ──
        b"XADD" => commands::xadd(&cmd.argv, store, w).await,
        b"XLEN" => commands::xlen(&cmd.argv, store, w).await,
        // ── string extras ──
        b"SETNX" => commands::setnx(&cmd.argv, store, w).await,
        b"GETSET" => commands::getset(&cmd.argv, store, w).await,
        b"GETDEL" => commands::getdel(&cmd.argv, store, w).await,
        b"STRLEN" => commands::strlen(&cmd.argv, store, w).await,
        b"APPEND" => commands::append(&cmd.argv, store, w).await,
        b"GETRANGE" | b"SUBSTR" => commands::getrange(&cmd.argv, store, w).await,
        b"BITCOUNT" => commands::bitcount(&cmd.argv, store, w).await,
        // ── hash extras ──
        b"HMGET" => commands::hmget(&cmd.argv, store, w).await,
        b"HMSET" => commands::hset(&cmd.argv, store, w).await, // alias — same impl
        b"HINCRBY" => commands::hincrby(&cmd.argv, store, w).await,
        b"HSETNX" => commands::hsetnx(&cmd.argv, store, w).await,
        // ── zset extras ──
        b"ZRANGEBYSCORE" => commands::zrangebyscore(&cmd.argv, store, w).await,
        b"ZRANK" => commands::zrank(&cmd.argv, store, w).await,
        b"ZREVRANK" => commands::zrevrank(&cmd.argv, store, w).await,
        // ── list extras ──
        b"LSET" => commands::lset(&cmd.argv, store, w).await,
        b"LREM" => commands::lrem(&cmd.argv, store, w).await,
        b"LTRIM" => commands::ltrim(&cmd.argv, store, w).await,
        b"LINSERT" => commands::linsert(&cmd.argv, store, w).await,
        // ── set extras ──
        b"SRANDMEMBER" => commands::srandmember(&cmd.argv, store, w).await,
        b"SMOVE" => commands::smove(&cmd.argv, store, w).await,
        // ── server / TTL ──
        b"TYPE" => commands::type_of(&cmd.argv, store, w).await,
        b"TTL" => commands::ttl(&cmd.argv, store, w).await,
        b"PTTL" => commands::pttl(&cmd.argv, store, w).await,
        b"EXPIRE" => commands::expire(&cmd.argv, store, w).await,
        b"PEXPIRE" => commands::pexpire(&cmd.argv, store, w).await,
        b"PERSIST" => commands::persist(&cmd.argv, store, w).await,
        b"DBSIZE" => commands::dbsize(&cmd.argv, store, w).await,
        b"RANDOMKEY" => commands::randomkey(&cmd.argv, store, w).await,
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
            // dispatch_local is only called by handle() AFTER
            // is_local() returned true. Reaching this arm means the
            // is_local table is out of sync with this match.
            unreachable!("dispatch_local called for non-local command: {:?}", name);
        }
    }
}
