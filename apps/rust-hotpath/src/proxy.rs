//! Proxy mode — forward commands the Rust hot path doesn't implement
//! (AI commands like `SEMANTIC_GET`, `MEMORY.QUERY`, `TOOL.GET`,
//! `GUARD.CHECK`, etc.; advanced standard commands like `XADD`,
//! `EVAL`, `SUBSCRIBE`) to the upstream Go server.
//!
//! Per-connection model:
//!   - Each client conn gets a *lazy* upstream conn (only opened on
//!     the first command we need to forward — apps that only use
//!     fast-path commands never pay the upstream connection cost).
//!   - When we forward, we re-serialize the parsed argv as RESP
//!     bulk-string array (zero-copy from the original Bytes), then
//!     stream the upstream's reply bytes back to the client.
//!   - Reply ordering: we serialize one command at a time per conn
//!     when forwarding (no pipelining across the local/proxy
//!     boundary). Local commands still pipeline normally — they
//!     dispatch + reply before we even consider the next read.
//!
//! Failure handling: if the upstream conn fails mid-request we
//! return -ERR back to the client and drop our upstream conn so
//! the next forwarded command will reconnect. The client doesn't
//! see a hang.

use crate::resp::Command;
use bytes::{BufMut, BytesMut};
use std::io;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

/// UpstreamConn wraps the proxy-mode TCP conn to the Go server. We
/// hold its read + write halves separately so reply parsing can read
/// while a future write is in flight (matters for big bulk replies).
pub struct UpstreamConn {
    addr: String,
    sock: Option<TcpStream>,
    /// Re-usable buffer for parsing reply frames so we don't malloc
    /// every reply.
    buf: BytesMut,
}

impl UpstreamConn {
    pub fn new(addr: String) -> Self {
        UpstreamConn {
            addr,
            sock: None,
            buf: BytesMut::with_capacity(8 * 1024),
        }
    }

    /// connect_lazy opens the upstream socket on first use. Return
    /// io::Error if the upstream is unreachable so the caller can
    /// surface -ERR to the client.
    async fn ensure(&mut self) -> io::Result<&mut TcpStream> {
        if self.sock.is_none() {
            let s = TcpStream::connect(&self.addr).await?;
            let _ = s.set_nodelay(true);
            self.sock = Some(s);
        }
        Ok(self.sock.as_mut().unwrap())
    }

    /// Drop the cached upstream conn — used after an I/O error so
    /// the next forwarded command reconnects.
    fn reset(&mut self) {
        self.sock = None;
    }

    /// Forward the parsed `cmd` to upstream, read the entire RESP
    /// reply, and write it to `client_w`. The reply may be any RESP
    /// type (simple string, error, integer, bulk, array, nested).
    pub async fn forward<W: AsyncWriteExt + Unpin>(
        &mut self,
        cmd: &Command,
        client_w: &mut W,
    ) -> io::Result<()> {
        // Serialize argv as a RESP array of bulk strings.
        // Zero-copy from the original Bytes — we're just emitting
        // headers + the raw arg bytes, not copying argv.
        let mut header = BytesMut::with_capacity(64);
        write_array_header(&mut header, cmd.argv.len() as i64);
        let upstream = match self.ensure().await {
            Ok(s) => s,
            Err(_) => {
                client_w
                    .write_all(b"-ERR upstream unreachable\r\n")
                    .await?;
                return Ok(());
            }
        };
        if let Err(e) = upstream.write_all(&header).await {
            self.reset();
            client_w
                .write_all(format!("-ERR upstream write: {e}\r\n").as_bytes())
                .await?;
            return Ok(());
        }
        for arg in &cmd.argv {
            let mut hdr = BytesMut::with_capacity(16);
            write_bulk_header(&mut hdr, arg.len() as i64);
            if upstream.write_all(&hdr).await.is_err()
                || upstream.write_all(arg).await.is_err()
                || upstream.write_all(b"\r\n").await.is_err()
            {
                self.reset();
                client_w
                    .write_all(b"-ERR upstream write failed\r\n")
                    .await?;
                return Ok(());
            }
        }

        // Read the upstream reply, stream it to the client. We need
        // to know how many bytes the reply is so we don't keep
        // reading after it. Parse the leading byte to know the
        // shape, then consume accordingly.
        match self.read_one_reply().await {
            Ok(reply) => client_w.write_all(&reply).await,
            Err(e) => {
                self.reset();
                client_w
                    .write_all(format!("-ERR upstream read: {e}\r\n").as_bytes())
                    .await
            }
        }
    }

    /// Read a complete RESP reply from the upstream socket. Returns
    /// the raw reply bytes (everything from the type prefix through
    /// the trailing CRLF, including all nested structures).
    async fn read_one_reply(&mut self) -> io::Result<BytesMut> {
        loop {
            // Try to parse a complete reply from what we already have.
            if let Some(end) = scan_reply_end(&self.buf) {
                let reply = self.buf.split_to(end);
                return Ok(reply);
            }
            // Not enough data — read more. Split-borrow self.sock and
            // self.buf to avoid the dual mutable borrow on `self`.
            let sock = self.sock.as_mut().ok_or_else(|| {
                io::Error::new(io::ErrorKind::NotConnected, "upstream gone")
            })?;
            let n = sock.read_buf(&mut self.buf).await?;
            if n == 0 {
                return Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "upstream closed",
                ));
            }
        }
    }
}

/// scan_reply_end returns the byte length of the first complete
/// RESP reply in `buf`, or None if the reply isn't fully buffered.
/// Recursive on arrays — handles nested structures up to any depth.
fn scan_reply_end(buf: &[u8]) -> Option<usize> {
    scan_one(buf, 0)
}

fn scan_one(buf: &[u8], start: usize) -> Option<usize> {
    if start >= buf.len() {
        return None;
    }
    match buf[start] {
        b'+' | b'-' | b':' => {
            // Simple-string / error / integer: ends at \r\n
            let crlf = find_crlf(buf, start + 1)?;
            Some(crlf + 2)
        }
        b'$' => {
            // Bulk string: $N\r\n<N bytes>\r\n  (or $-1\r\n for nil)
            let crlf = find_crlf(buf, start + 1)?;
            let n = parse_int(&buf[start + 1..crlf])?;
            if n < 0 {
                return Some(crlf + 2); // null bulk
            }
            let body_end = crlf + 2 + n as usize + 2;
            if body_end > buf.len() {
                return None;
            }
            Some(body_end)
        }
        b'*' => {
            // Array: *N\r\n<N elements>
            let crlf = find_crlf(buf, start + 1)?;
            let n = parse_int(&buf[start + 1..crlf])?;
            if n < 0 {
                return Some(crlf + 2); // null array
            }
            let mut cursor = crlf + 2;
            for _ in 0..n {
                cursor = scan_one(buf, cursor)?;
            }
            Some(cursor)
        }
        _ => None,
    }
}

fn find_crlf(buf: &[u8], from: usize) -> Option<usize> {
    for i in from..buf.len().saturating_sub(1) {
        if buf[i] == b'\r' && buf[i + 1] == b'\n' {
            return Some(i);
        }
    }
    None
}

fn parse_int(s: &[u8]) -> Option<i64> {
    if s.is_empty() {
        return None;
    }
    let (neg, rest) = match s[0] {
        b'-' => (true, &s[1..]),
        b'+' => (false, &s[1..]),
        _ => (false, s),
    };
    if rest.is_empty() {
        return None;
    }
    let mut n: i64 = 0;
    for &b in rest {
        if !(b'0'..=b'9').contains(&b) {
            return None;
        }
        n = n.checked_mul(10)?.checked_add((b - b'0') as i64)?;
    }
    Some(if neg { -n } else { n })
}

fn write_array_header(b: &mut BytesMut, n: i64) {
    b.put_u8(b'*');
    write_int(b, n);
    b.put_slice(b"\r\n");
}

fn write_bulk_header(b: &mut BytesMut, n: i64) {
    b.put_u8(b'$');
    write_int(b, n);
    b.put_slice(b"\r\n");
}

fn write_int(b: &mut BytesMut, n: i64) {
    let mut buf = [0u8; 20];
    let mut i = buf.len();
    let (neg, mut n) = if n < 0 { (true, -n) } else { (false, n) };
    if n == 0 {
        i -= 1;
        buf[i] = b'0';
    } else {
        while n > 0 {
            i -= 1;
            buf[i] = b'0' + (n % 10) as u8;
            n /= 10;
        }
    }
    if neg {
        b.put_u8(b'-');
    }
    b.put_slice(&buf[i..]);
}

#[cfg(test)]
mod tests {
    use super::*;
    use bytes::BufMut;

    #[test]
    fn scans_simple_string() {
        let mut b = BytesMut::new();
        b.put_slice(b"+OK\r\n");
        assert_eq!(scan_reply_end(&b), Some(5));
    }

    #[test]
    fn scans_integer() {
        let mut b = BytesMut::new();
        b.put_slice(b":42\r\n");
        assert_eq!(scan_reply_end(&b), Some(5));
    }

    #[test]
    fn scans_bulk_string() {
        let mut b = BytesMut::new();
        b.put_slice(b"$5\r\nhello\r\n");
        assert_eq!(scan_reply_end(&b), Some(11));
    }

    #[test]
    fn scans_null_bulk() {
        let mut b = BytesMut::new();
        b.put_slice(b"$-1\r\n");
        assert_eq!(scan_reply_end(&b), Some(5));
    }

    #[test]
    fn scans_simple_array() {
        let mut b = BytesMut::new();
        b.put_slice(b"*3\r\n:1\r\n:2\r\n$3\r\nfoo\r\n");
        assert_eq!(scan_reply_end(&b), Some(21));
    }

    #[test]
    fn scans_nested_array() {
        let mut b = BytesMut::new();
        b.put_slice(b"*2\r\n*2\r\n+a\r\n+b\r\n*1\r\n:7\r\n");
        assert_eq!(scan_reply_end(&b), Some(b.len()));
    }

    #[test]
    fn scans_returns_none_on_partial() {
        let mut b = BytesMut::new();
        b.put_slice(b"$5\r\nhel"); // missing 2 chars + crlf
        assert_eq!(scan_reply_end(&b), None);
    }

    #[test]
    fn write_int_handles_zero_and_negative() {
        let mut b = BytesMut::new();
        write_int(&mut b, 0);
        assert_eq!(&b[..], b"0");
        b.clear();
        write_int(&mut b, -42);
        assert_eq!(&b[..], b"-42");
        b.clear();
        write_int(&mut b, 12345);
        assert_eq!(&b[..], b"12345");
    }
}
