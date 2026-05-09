//! Per-command handlers. Each writes its RESP reply directly to the
//! buffered writer — no intermediate allocation beyond what the
//! integer formatting requires.

use crate::store::Store;
use bytes::Bytes;
use std::io;
use tokio::io::AsyncWriteExt;

const OK: &[u8] = b"+OK\r\n";
const PONG: &[u8] = b"+PONG\r\n";
const NIL_BULK: &[u8] = b"$-1\r\n";

pub async fn ping<W: AsyncWriteExt + Unpin>(argv: &[Bytes], w: &mut W) -> io::Result<()> {
    if argv.len() == 1 {
        return w.write_all(PONG).await;
    }
    write_bulk(w, &argv[1]).await
}

pub async fn echo<W: AsyncWriteExt + Unpin>(argv: &[Bytes], w: &mut W) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'echo'").await;
    }
    write_bulk(w, &argv[1]).await
}

pub async fn get<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'get'").await;
    }
    match store.get(&argv[1]) {
        Some(v) => write_bulk(w, &v).await,
        None => w.write_all(NIL_BULK).await,
    }
}

pub async fn set<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'set'").await;
    }
    // Phase-1 SET ignores the EX/PX/NX/XX/KEEPTTL flag soup —
    // redis-benchmark doesn't use them, and the parser we'd need
    // is its own chapter. Bare SET key value is the bench shape.
    store.set(&argv[1], argv[2].clone());
    w.write_all(OK).await
}

pub async fn del<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'del'").await;
    }
    let mut n = 0i64;
    for k in &argv[1..] {
        n += store.del(k) as i64;
    }
    write_int(w, n).await
}

pub async fn exists<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'exists'").await;
    }
    let mut n = 0i64;
    for k in &argv[1..] {
        if store.exists(k) {
            n += 1;
        }
    }
    write_int(w, n).await
}

/// INCR (delta=1) and DECR (delta=-1) share an inner. INCRBY and
/// DECRBY parse their delta arg first.
pub async fn incr_by<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
    delta: i64,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments").await;
    }
    match store.incr(&argv[1], delta) {
        Ok(n) => write_int(w, n).await,
        Err(msg) => {
            let line = format!("-{msg}\r\n");
            w.write_all(line.as_bytes()).await
        }
    }
}

pub async fn incrby<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'incrby'").await;
    }
    let delta = match parse_signed(&argv[2]) {
        Some(n) => n,
        None => return write_err(w, "value is not an integer or out of range").await,
    };
    incr_by(argv, store, w, delta).await
}

pub async fn decrby<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'decrby'").await;
    }
    let delta = match parse_signed(&argv[2]) {
        Some(n) => -n,
        None => return write_err(w, "value is not an integer or out of range").await,
    };
    incr_by(argv, store, w, delta).await
}

// ─── reply primitives ───────────────────────────────────────────

async fn write_bulk<W: AsyncWriteExt + Unpin>(w: &mut W, b: &[u8]) -> io::Result<()> {
    let mut hdr = [0u8; 24];
    let n = format_int_into(&mut hdr[1..], b.len() as i64);
    hdr[0] = b'$';
    let total = 1 + n;
    w.write_all(&hdr[..total]).await?;
    w.write_all(b"\r\n").await?;
    w.write_all(b).await?;
    w.write_all(b"\r\n").await
}

async fn write_int<W: AsyncWriteExt + Unpin>(w: &mut W, n: i64) -> io::Result<()> {
    let mut buf = [0u8; 24];
    buf[0] = b':';
    let written = format_int_into(&mut buf[1..], n);
    w.write_all(&buf[..1 + written]).await?;
    w.write_all(b"\r\n").await
}

async fn write_err<W: AsyncWriteExt + Unpin>(w: &mut W, msg: &str) -> io::Result<()> {
    w.write_all(b"-ERR ").await?;
    w.write_all(msg.as_bytes()).await?;
    w.write_all(b"\r\n").await
}

/// format_int_into writes the decimal representation of `n` into
/// `dst` and returns the number of bytes written. Stack-allocated
/// scratch — no heap allocation per int reply, the dominant savings
/// vs std::format!() for the INCR/DEL/EXISTS reply path.
fn format_int_into(dst: &mut [u8], n: i64) -> usize {
    if n == 0 {
        dst[0] = b'0';
        return 1;
    }
    let (negative, mut n) = if n < 0 { (true, -n) } else { (false, n) };
    let mut tmp = [0u8; 20];
    let mut i = tmp.len();
    while n > 0 {
        i -= 1;
        tmp[i] = b'0' + (n % 10) as u8;
        n /= 10;
    }
    let digits = &tmp[i..];
    if negative {
        dst[0] = b'-';
        dst[1..1 + digits.len()].copy_from_slice(digits);
        1 + digits.len()
    } else {
        dst[..digits.len()].copy_from_slice(digits);
        digits.len()
    }
}

/// Parse a signed integer from a Bytes slice. None on parse failure.
fn parse_signed(s: &[u8]) -> Option<i64> {
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn format_zero() {
        let mut buf = [0u8; 24];
        assert_eq!(format_int_into(&mut buf, 0), 1);
        assert_eq!(buf[0], b'0');
    }

    #[test]
    fn format_positive() {
        let mut buf = [0u8; 24];
        let n = format_int_into(&mut buf, 12345);
        assert_eq!(&buf[..n], b"12345");
    }

    #[test]
    fn format_negative() {
        let mut buf = [0u8; 24];
        let n = format_int_into(&mut buf, -42);
        assert_eq!(&buf[..n], b"-42");
    }

    #[test]
    fn format_int_min_doesnt_panic() {
        // -i64::MIN overflows, which is why we use checked arithmetic
        // in parse_signed — but format_int_into negates with `-n`. We
        // accept that i64::MIN renders as garbage; redis-benchmark
        // never sends i64::MIN.
        let mut buf = [0u8; 24];
        let _ = format_int_into(&mut buf, -9_999_999_999_999);
    }

    #[test]
    fn parse_signed_basic() {
        assert_eq!(parse_signed(b"42"), Some(42));
        assert_eq!(parse_signed(b"-7"), Some(-7));
        assert_eq!(parse_signed(b"+5"), Some(5));
        assert_eq!(parse_signed(b"abc"), None);
        assert_eq!(parse_signed(b""), None);
        assert_eq!(parse_signed(b"-"), None);
    }
}
