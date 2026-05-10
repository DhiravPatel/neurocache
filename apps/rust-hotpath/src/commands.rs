//! Per-command RESP handlers. Each writes its reply directly to the
//! buffered writer — no intermediate allocation beyond what integer
//! formatting + multi-bulk array headers require.

use crate::store::{Store, StoreError};
use bytes::Bytes;
use std::io;
use tokio::io::AsyncWriteExt;

const OK: &[u8] = b"+OK\r\n";
const PONG: &[u8] = b"+PONG\r\n";
const NIL_BULK: &[u8] = b"$-1\r\n";
const NIL_ARRAY: &[u8] = b"*-1\r\n";
const EMPTY_ARRAY: &[u8] = b"*0\r\n";

const WRONGTYPE_MSG: &str =
    "WRONGTYPE Operation against a key holding the wrong kind of value";
const NOT_INT_MSG: &str = "value is not an integer or out of range";

// ─── connection / server ─────────────────────────────────────────

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

// ─── strings ─────────────────────────────────────────────────────

pub async fn get<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'get'").await;
    }
    match store.get(&argv[1]) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
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
        Err(e) => write_store_err(w, e).await,
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
        None => return write_err(w, NOT_INT_MSG).await,
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
        None => return write_err(w, NOT_INT_MSG).await,
    };
    incr_by(argv, store, w, delta).await
}

pub async fn mset<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 || argv.len() % 2 == 0 {
        return write_err(w, "wrong number of arguments for 'mset'").await;
    }
    let mut pairs = Vec::with_capacity((argv.len() - 1) / 2);
    for i in (1..argv.len()).step_by(2) {
        pairs.push((argv[i].to_vec(), argv[i + 1].clone()));
    }
    store.mset(&pairs);
    w.write_all(OK).await
}

pub async fn mget<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'mget'").await;
    }
    let keys: Vec<&[u8]> = argv[1..].iter().map(|b| b.as_ref()).collect();
    let results = store.mget(&keys);
    write_array_header(w, results.len() as i64).await?;
    for v in results {
        match v {
            Some(b) => write_bulk(w, &b).await?,
            None => w.write_all(NIL_BULK).await?,
        }
    }
    Ok(())
}

// ─── lists ───────────────────────────────────────────────────────

pub async fn lpush<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    push_inner(argv, store, w, true).await
}

pub async fn rpush<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    push_inner(argv, store, w, false).await
}

async fn push_inner<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
    front: bool,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments").await;
    }
    let values: Vec<Bytes> = argv[2..].to_vec();
    let r = if front {
        store.lpush(&argv[1], &values)
    } else {
        store.rpush(&argv[1], &values)
    };
    match r {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn lpop<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'lpop'").await;
    }
    match store.lpop(&argv[1]) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn rpop<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'rpop'").await;
    }
    match store.rpop(&argv[1]) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn llen<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'llen'").await;
    }
    match store.llen(&argv[1]) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn lrange<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 4 {
        return write_err(w, "wrong number of arguments for 'lrange'").await;
    }
    let start = match parse_signed(&argv[2]) {
        Some(n) => n,
        None => return write_err(w, NOT_INT_MSG).await,
    };
    let stop = match parse_signed(&argv[3]) {
        Some(n) => n,
        None => return write_err(w, NOT_INT_MSG).await,
    };
    match store.lrange(&argv[1], start, stop) {
        Ok(items) => {
            write_array_header(w, items.len() as i64).await?;
            for v in items {
                write_bulk(w, &v).await?;
            }
            Ok(())
        }
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn lindex<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'lindex'").await;
    }
    let i = match parse_signed(&argv[2]) {
        Some(n) => n,
        None => return write_err(w, NOT_INT_MSG).await,
    };
    match store.lindex(&argv[1], i) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
    }
}

// ─── hashes ──────────────────────────────────────────────────────

pub async fn hset<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 4 || (argv.len() % 2) != 0 {
        return write_err(w, "wrong number of arguments for 'hset'").await;
    }
    let mut pairs = Vec::with_capacity((argv.len() - 2) / 2);
    for i in (2..argv.len()).step_by(2) {
        pairs.push((argv[i].to_vec(), argv[i + 1].clone()));
    }
    match store.hset(&argv[1], &pairs) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hget<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'hget'").await;
    }
    match store.hget(&argv[1], &argv[2]) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hdel<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'hdel'").await;
    }
    let fields: Vec<&[u8]> = argv[2..].iter().map(|b| b.as_ref()).collect();
    match store.hdel(&argv[1], &fields) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hlen<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'hlen'").await;
    }
    match store.hlen(&argv[1]) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hexists<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'hexists'").await;
    }
    match store.hexists(&argv[1], &argv[2]) {
        Ok(true) => write_int(w, 1).await,
        Ok(false) => write_int(w, 0).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hgetall<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'hgetall'").await;
    }
    match store.hgetall(&argv[1]) {
        Ok(pairs) => {
            write_array_header(w, (pairs.len() * 2) as i64).await?;
            for (f, v) in pairs {
                write_bulk(w, &f).await?;
                write_bulk(w, &v).await?;
            }
            Ok(())
        }
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hkeys<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'hkeys'").await;
    }
    match store.hkeys(&argv[1]) {
        Ok(keys) => {
            write_array_header(w, keys.len() as i64).await?;
            for k in keys {
                write_bulk(w, &k).await?;
            }
            Ok(())
        }
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn hvals<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'hvals'").await;
    }
    match store.hvals(&argv[1]) {
        Ok(vals) => {
            write_array_header(w, vals.len() as i64).await?;
            for v in vals {
                write_bulk(w, &v).await?;
            }
            Ok(())
        }
        Err(e) => write_store_err(w, e).await,
    }
}

// ─── sets ────────────────────────────────────────────────────────

pub async fn sadd<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'sadd'").await;
    }
    let members: Vec<&[u8]> = argv[2..].iter().map(|b| b.as_ref()).collect();
    match store.sadd(&argv[1], &members) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn srem<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'srem'").await;
    }
    let members: Vec<&[u8]> = argv[2..].iter().map(|b| b.as_ref()).collect();
    match store.srem(&argv[1], &members) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn sismember<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 3 {
        return write_err(w, "wrong number of arguments for 'sismember'").await;
    }
    match store.sismember(&argv[1], &argv[2]) {
        Ok(true) => write_int(w, 1).await,
        Ok(false) => write_int(w, 0).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn scard<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'scard'").await;
    }
    match store.scard(&argv[1]) {
        Ok(n) => write_int(w, n as i64).await,
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn smembers<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'smembers'").await;
    }
    match store.smembers(&argv[1]) {
        Ok(members) => {
            write_array_header(w, members.len() as i64).await?;
            for m in members {
                write_bulk(w, &m).await?;
            }
            Ok(())
        }
        Err(e) => write_store_err(w, e).await,
    }
}

pub async fn spop<W: AsyncWriteExt + Unpin>(
    argv: &[Bytes],
    store: &Store,
    w: &mut W,
) -> io::Result<()> {
    if argv.len() < 2 {
        return write_err(w, "wrong number of arguments for 'spop'").await;
    }
    match store.spop(&argv[1]) {
        Ok(Some(v)) => write_bulk(w, &v).await,
        Ok(None) => w.write_all(NIL_BULK).await,
        Err(e) => write_store_err(w, e).await,
    }
}

// ─── reply primitives ───────────────────────────────────────────

async fn write_bulk<W: AsyncWriteExt + Unpin>(w: &mut W, b: &[u8]) -> io::Result<()> {
    let mut hdr = [0u8; 24];
    hdr[0] = b'$';
    let n = format_int_into(&mut hdr[1..], b.len() as i64);
    w.write_all(&hdr[..1 + n]).await?;
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

async fn write_array_header<W: AsyncWriteExt + Unpin>(w: &mut W, n: i64) -> io::Result<()> {
    if n == 0 {
        return w.write_all(EMPTY_ARRAY).await;
    }
    if n < 0 {
        return w.write_all(NIL_ARRAY).await;
    }
    let mut buf = [0u8; 24];
    buf[0] = b'*';
    let written = format_int_into(&mut buf[1..], n);
    w.write_all(&buf[..1 + written]).await?;
    w.write_all(b"\r\n").await
}

async fn write_err<W: AsyncWriteExt + Unpin>(w: &mut W, msg: &str) -> io::Result<()> {
    w.write_all(b"-ERR ").await?;
    w.write_all(msg.as_bytes()).await?;
    w.write_all(b"\r\n").await
}

async fn write_store_err<W: AsyncWriteExt + Unpin>(
    w: &mut W,
    e: StoreError,
) -> io::Result<()> {
    match e {
        StoreError::WrongType => {
            w.write_all(b"-").await?;
            w.write_all(WRONGTYPE_MSG.as_bytes()).await?;
            w.write_all(b"\r\n").await
        }
        StoreError::NotInteger => {
            w.write_all(b"-ERR ").await?;
            w.write_all(NOT_INT_MSG.as_bytes()).await?;
            w.write_all(b"\r\n").await
        }
        StoreError::NoSuchKey => write_err(w, "no such key").await,
    }
}

/// format_int_into writes the decimal representation of `n` into
/// `dst` and returns the byte count. Stack-only — no heap alloc per
/// int reply.
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

/// Parse a signed i64 from a Bytes slice. None on parse failure.
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
    fn parse_signed_basic() {
        assert_eq!(parse_signed(b"42"), Some(42));
        assert_eq!(parse_signed(b"-7"), Some(-7));
        assert_eq!(parse_signed(b"+5"), Some(5));
        assert_eq!(parse_signed(b"abc"), None);
        assert_eq!(parse_signed(b""), None);
        assert_eq!(parse_signed(b"-"), None);
    }
}
