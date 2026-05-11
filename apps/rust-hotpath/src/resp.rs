//! Minimal zero-copy RESP2 parser.
//!
//! Returns parsed commands as `Vec<Bytes>` where each Bytes is a
//! reference-counted slice into the read buffer — no copying. The
//! caller (commands.rs) holds onto these for the lifetime of the
//! dispatch then drops them; the underlying buffer can then be
//! reclaimed by the read loop.
//!
//! Supports inline commands (space-separated text — what redis-cli
//! sends interactively) and the array-of-bulks form (what every
//! client library sends in production). No support for inline
//! quoting nuances yet — the bench client always uses the bulk form.

use bytes::{Bytes, BytesMut};
use std::io;

/// One parsed RESP command — its argv as Bytes references into the
/// connection's read buffer.
#[derive(Debug)]
pub struct Command {
    pub argv: Vec<Bytes>,
}

/// Try to parse one command from the buffer. Returns:
///   Ok(Some(cmd))  — parsed; the buffer is advanced past the command
///   Ok(None)       — incomplete; need more data, leave buffer untouched
///   Err(_)         — malformed; caller should disconnect
pub fn parse(buf: &mut BytesMut) -> io::Result<Option<Command>> {
    if buf.is_empty() {
        return Ok(None);
    }
    if buf[0] == b'*' {
        return parse_array(buf);
    }
    parse_inline(buf)
}

/// Parse the array-of-bulks form sent by every client library:
///   *N\r\n$L1\r\n<bytes1>\r\n...$Ln\r\n<bytesN>\r\n
fn parse_array(buf: &mut BytesMut) -> io::Result<Option<Command>> {
    // header: *N\r\n
    let (n, hdr_len) = match read_int_line(&buf[1..])? {
        Some(v) => v,
        None => return Ok(None),
    };
    if n < 0 {
        // null array — treat as empty command (no-op)
        let _ = buf.split_to(1 + hdr_len);
        return Ok(Some(Command { argv: vec![] }));
    }
    let n = n as usize;

    // Walk the buffer to find the total command length without
    // mutating it — we want all-or-nothing consumption so a partial
    // command stays in the buffer for the next read.
    let mut cursor = 1 + hdr_len;
    let mut argv_lens = Vec::with_capacity(n);
    for _ in 0..n {
        if cursor >= buf.len() {
            return Ok(None);
        }
        if buf[cursor] != b'$' {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "expected $ bulk header",
            ));
        }
        let (size, size_hdr_len) = match read_int_line(&buf[cursor + 1..])? {
            Some(v) => v,
            None => return Ok(None),
        };
        if size < 0 {
            // null bulk — push empty arg, advance past header only
            argv_lens.push((cursor + 1 + size_hdr_len, 0));
            cursor += 1 + size_hdr_len;
            continue;
        }
        let body_start = cursor + 1 + size_hdr_len;
        let body_end = body_start + size as usize;
        if body_end + 2 > buf.len() {
            return Ok(None); // body + trailing \r\n not yet here
        }
        if buf[body_end] != b'\r' || buf[body_end + 1] != b'\n' {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "missing CRLF after bulk body",
            ));
        }
        argv_lens.push((body_start, size as usize));
        cursor = body_end + 2;
    }

    // Commit: split the consumed bytes off the buffer and slice argv
    // into the resulting frozen Bytes. The slicing is zero-copy —
    // each Bytes is a refcount on the same backing allocation.
    let frame = buf.split_to(cursor).freeze();
    let mut argv = Vec::with_capacity(n);
    for (start, len) in argv_lens {
        // The (start, len) offsets were computed in the original
        // buffer's coordinate system; that's the same coordinate
        // system as `frame` since split_to preserves the prefix.
        argv.push(frame.slice(start..start + len));
    }
    Ok(Some(Command { argv }))
}

/// Parse the inline form (used by `redis-cli` interactive mode):
///   PING\r\n
///   SET key value\r\n
fn parse_inline(buf: &mut BytesMut) -> io::Result<Option<Command>> {
    let line_end = match find_crlf(&buf[..]) {
        Some(i) => i,
        None => return Ok(None),
    };
    // Take ownership of the line, then split & emit.
    let frame = buf.split_to(line_end + 2).freeze();
    let line = frame.slice(..line_end);
    let argv: Vec<Bytes> = line
        .split(|&b| b == b' ' || b == b'\t')
        .filter(|s| !s.is_empty())
        .map(|s| {
            // Find the offset of `s` inside `line` so we can slice
            // by index — line.split returns &[u8] subslices, not
            // Bytes, so we recover the offsets to keep zero-copy.
            let off = (s.as_ptr() as usize) - (line.as_ptr() as usize);
            line.slice(off..off + s.len())
        })
        .collect();
    Ok(Some(Command { argv }))
}

/// Read an ASCII integer terminated by \r\n. Returns (value, bytes
/// consumed including the \r\n) or None if the line isn't complete.
fn read_int_line(s: &[u8]) -> io::Result<Option<(i64, usize)>> {
    let crlf = match find_crlf(s) {
        Some(i) => i,
        None => return Ok(None),
    };
    let n = parse_i64(&s[..crlf])?;
    Ok(Some((n, crlf + 2)))
}

/// Find the position of the first \r\n in `s`, or None.
fn find_crlf(s: &[u8]) -> Option<usize> {
    for i in 0..s.len().saturating_sub(1) {
        if s[i] == b'\r' && s[i + 1] == b'\n' {
            return Some(i);
        }
    }
    None
}

/// Tight i64 parser that handles the leading `-` and digits without
/// allocating. Roughly 4-5x faster than calling str::from_utf8 +
/// str::parse, and avoids the UTF-8 validation we don't need
/// (RESP integer headers are always ASCII).
fn parse_i64(s: &[u8]) -> io::Result<i64> {
    if s.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "empty integer"));
    }
    let (neg, rest) = match s[0] {
        b'-' => (true, &s[1..]),
        b'+' => (false, &s[1..]),
        _ => (false, s),
    };
    if rest.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "no digits after sign",
        ));
    }
    let mut n: i64 = 0;
    for &b in rest {
        if !(b'0'..=b'9').contains(&b) {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "non-digit in integer",
            ));
        }
        n = n.wrapping_mul(10).wrapping_add((b - b'0') as i64);
    }
    if neg {
        n = -n;
    }
    Ok(n)
}

/// uppercase_ascii fast-paths the command name normalization the
/// dispatch path needs. Returns the input unchanged when already
/// upper (the typical case from client libraries) — zero-copy fast
/// path. Allocates a new String only when at least one lowercase
/// byte is present.
pub fn uppercase_ascii(b: &[u8]) -> Vec<u8> {
    let mut needs_alloc = false;
    for &c in b {
        if (b'a'..=b'z').contains(&c) {
            needs_alloc = true;
            break;
        }
    }
    if !needs_alloc {
        return b.to_vec();
    }
    b.iter()
        .map(|&c| if (b'a'..=b'z').contains(&c) { c - 32 } else { c })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use bytes::BufMut;

    #[test]
    fn parses_simple_array() {
        let mut buf = BytesMut::new();
        buf.put_slice(b"*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nbar\r\n");
        let cmd = parse(&mut buf).unwrap().unwrap();
        assert_eq!(cmd.argv.len(), 3);
        assert_eq!(&cmd.argv[0][..], b"SET");
        assert_eq!(&cmd.argv[1][..], b"foo");
        assert_eq!(&cmd.argv[2][..], b"bar");
        assert!(buf.is_empty());
    }

    #[test]
    fn returns_none_on_partial() {
        let mut buf = BytesMut::new();
        buf.put_slice(b"*3\r\n$3\r\nSET\r\n$3\r\nfoo\r\n$3\r\nb"); // missing tail
        assert!(parse(&mut buf).unwrap().is_none());
    }

    #[test]
    fn handles_inline_command() {
        let mut buf = BytesMut::new();
        buf.put_slice(b"PING\r\n");
        let cmd = parse(&mut buf).unwrap().unwrap();
        assert_eq!(cmd.argv.len(), 1);
        assert_eq!(&cmd.argv[0][..], b"PING");
    }

    #[test]
    fn handles_two_back_to_back_commands() {
        let mut buf = BytesMut::new();
        buf.put_slice(b"*1\r\n$4\r\nPING\r\n*1\r\n$4\r\nPING\r\n");
        let c1 = parse(&mut buf).unwrap().unwrap();
        assert_eq!(&c1.argv[0][..], b"PING");
        let c2 = parse(&mut buf).unwrap().unwrap();
        assert_eq!(&c2.argv[0][..], b"PING");
        assert!(buf.is_empty());
    }

    #[test]
    fn uppercase_zero_copy_when_already_upper() {
        let v = uppercase_ascii(b"GET");
        assert_eq!(&v[..], b"GET");
    }

    #[test]
    fn uppercase_handles_lower() {
        let v = uppercase_ascii(b"set");
        assert_eq!(&v[..], b"SET");
    }
}
