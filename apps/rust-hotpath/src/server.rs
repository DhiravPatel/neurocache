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

use crate::commands;
use crate::resp::{parse, Command};
use crate::store::Store;
use bytes::BytesMut;
use std::io;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt, BufWriter};
use tokio::net::{TcpListener, TcpStream};

/// Run the server on `addr` until cancelled. The Store is shared
/// across every connection via Arc — cheap clone, all the actual
/// data lives behind the shard mutexes.
pub async fn run(addr: &str, store: Arc<Store>) -> io::Result<()> {
    let listener = TcpListener::bind(addr).await?;
    eprintln!("neurocache-hotpath listening on {addr}");
    loop {
        let (sock, _peer) = listener.accept().await?;
        // TCP_NODELAY mirrors what the Go server sets — pipelined
        // bursts shouldn't sit in Nagle's window.
        let _ = sock.set_nodelay(true);
        let store = store.clone();
        tokio::task::spawn_local(async move {
            if let Err(e) = handle(sock, store).await {
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
async fn handle(sock: TcpStream, store: Arc<Store>) -> io::Result<()> {
    let (mut rd, wr) = sock.into_split();
    // 64 KiB write buffer — same as the Go side. Big enough that 16
    // pipelined replies coalesce into one write syscall.
    let mut wr = BufWriter::with_capacity(64 * 1024, wr);
    // 64 KiB read buffer. We append into it; the parser splits frames
    // off when complete.
    let mut buf = BytesMut::with_capacity(64 * 1024);

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
                Some(cmd) => dispatch(&cmd, &store, &mut wr).await?,
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
/// the (uppercased) command name. The set of supported commands is
/// deliberately minimal — Phase 1 just needs the bench-critical
/// surface; more flow over from the Go side as we expand.
async fn dispatch<W: AsyncWriteExt + Unpin>(
    cmd: &Command,
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if cmd.argv.is_empty() {
        return Ok(());
    }
    // Uppercase the command name. We accept either case from the
    // wire (every client library sends upper, redis-cli sends lower).
    let name = crate::resp::uppercase_ascii(&cmd.argv[0]);
    match name.as_slice() {
        b"PING" => commands::ping(&cmd.argv, w).await,
        b"ECHO" => commands::echo(&cmd.argv, w).await,
        b"GET" => commands::get(&cmd.argv, store, w).await,
        b"SET" => commands::set(&cmd.argv, store, w).await,
        b"INCR" => commands::incr_by(&cmd.argv, store, w, 1).await,
        b"DECR" => commands::incr_by(&cmd.argv, store, w, -1).await,
        b"INCRBY" => commands::incrby(&cmd.argv, store, w).await,
        b"DECRBY" => commands::decrby(&cmd.argv, store, w).await,
        b"DEL" => commands::del(&cmd.argv, store, w).await,
        b"EXISTS" => commands::exists(&cmd.argv, store, w).await,
        b"COMMAND" => {
            // redis-benchmark sends COMMAND DOCS at startup. Reply
            // with an empty array; the client doesn't care about the
            // contents, only that it gets an array.
            w.write_all(b"*0\r\n").await
        }
        b"HELLO" => {
            // Bench tools send HELLO sometimes. Reply with a minimal
            // RESP2 map-as-array.
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
            // Phase-1 binary doesn't implement everything yet. Reply
            // with a clean error so callers don't hang.
            let msg = format!(
                "-ERR unknown command '{}' (Phase-1 hotpath supports PING/ECHO/GET/SET/INCR/DECR/INCRBY/DECRBY/DEL/EXISTS)\r\n",
                String::from_utf8_lossy(&cmd.argv[0])
            );
            w.write_all(msg.as_bytes()).await
        }
    }
}
