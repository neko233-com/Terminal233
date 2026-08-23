//! Small in-memory known-hosts store used by the Terminal233 puressh build.
//!
//! The application keeps host trust in memory only and drives the explicit confirmation flow in
//! `terminal233/src/domain.rs`. This module deliberately fails closed for certificate authorities;
//! the client runtime never enables certificate host keys in its allow-list.

#![allow(missing_docs)]

use std::{fs, io, path::Path};

#[derive(Debug)]
pub enum LookupResult {
    Match,
    Mismatch { expected: Vec<(String, Vec<u8>)> },
    Unknown,
}

#[derive(Default)]
pub struct KnownHosts {
    entries: Vec<Entry>,
}

struct Entry {
    host: String,
    port: u16,
    key_type: String,
    key_blob: Vec<u8>,
}

impl KnownHosts {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn lookup(&self, host: &str, port: u16, key_type: &str, key_blob: &[u8]) -> LookupResult {
        let matching: Vec<&Entry> = self
            .entries
            .iter()
            .filter(|entry| entry.host == host && entry.port == port)
            .collect();
        if matching.is_empty() {
            return LookupResult::Unknown;
        }
        if matching
            .iter()
            .any(|entry| entry.key_type == key_type && entry.key_blob == key_blob)
        {
            LookupResult::Match
        } else {
            LookupResult::Mismatch {
                expected: matching
                    .into_iter()
                    .map(|entry| (entry.key_type.clone(), entry.key_blob.clone()))
                    .collect(),
            }
        }
    }

    pub fn add(&mut self, host: &str, port: u16, key_type: &str, key_blob: &[u8], _hashed: bool) {
        self.entries.push(Entry {
            host: host.to_owned(),
            port,
            key_type: key_type.to_owned(),
            key_blob: key_blob.to_vec(),
        });
    }

    pub fn remove(&mut self, host: &str, port: u16) -> usize {
        let old_len = self.entries.len();
        self.entries
            .retain(|entry| entry.host != host || entry.port != port);
        old_len - self.entries.len()
    }

    pub fn load(path: impl AsRef<Path>) -> io::Result<Self> {
        let bytes = match fs::read(path) {
            Ok(bytes) => bytes,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Self::new()),
            Err(error) => return Err(error),
        };
        let mut result = Self::new();
        for line in String::from_utf8_lossy(&bytes).lines() {
            let mut fields = line.split_whitespace();
            let Some(host) = fields.next() else { continue };
            let Some(key_type) = fields.next() else {
                continue;
            };
            let Some(encoded) = fields.next() else {
                continue;
            };
            let Ok(key_blob) = base64_decode(encoded) else {
                continue;
            };
            let (host, port) = parse_host(host);
            result.add(host, port, key_type, &key_blob, false);
        }
        Ok(result)
    }

    pub fn save(&self, path: impl AsRef<Path>) -> io::Result<()> {
        let mut output = String::new();
        for entry in &self.entries {
            let host = if entry.port == 22 {
                entry.host.clone()
            } else {
                format!("[{}]:{}", entry.host, entry.port)
            };
            output.push_str(&format!(
                "{} {} {}\n",
                host,
                entry.key_type,
                base64_encode(&entry.key_blob)
            ));
        }
        fs::write(path, output)
    }

    pub fn verify_host_cert(
        &self,
        _host: &str,
        _port: u16,
        _cert: &crate::cert::Certificate,
        _allowed_ca_algos: &[&str],
        _now: u64,
    ) -> crate::Result<()> {
        Err(crate::error::Error::HostKeyRejected)
    }
}

fn parse_host(value: &str) -> (&str, u16) {
    if let Some(rest) = value.strip_prefix('[')
        && let Some((host, port)) = rest.split_once("]:")
        && let Ok(port) = port.parse()
    {
        return (host, port);
    }
    (value, 22)
}

fn base64_encode(value: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut output = String::with_capacity(value.len().div_ceil(3) * 4);
    for chunk in value.chunks(3) {
        let first = chunk[0] as usize;
        let second = chunk.get(1).copied().unwrap_or(0) as usize;
        let third = chunk.get(2).copied().unwrap_or(0) as usize;
        output.push(TABLE[first >> 2] as char);
        output.push(TABLE[((first & 3) << 4) | (second >> 4)] as char);
        output.push(if chunk.len() > 1 {
            TABLE[((second & 15) << 2) | (third >> 6)] as char
        } else {
            '='
        });
        output.push(if chunk.len() > 2 {
            TABLE[third & 63] as char
        } else {
            '='
        });
    }
    output
}

fn base64_decode(value: &str) -> Result<Vec<u8>, ()> {
    let bytes = value.as_bytes();
    if bytes.len() % 4 != 0 {
        return Err(());
    }
    let mut output = Vec::with_capacity(bytes.len() / 4 * 3);
    for chunk in bytes.chunks(4) {
        let a = decode_base64_byte(chunk[0])?;
        let b = decode_base64_byte(chunk[1])?;
        let c = if chunk[2] == b'=' {
            0
        } else {
            decode_base64_byte(chunk[2])?
        };
        let d = if chunk[3] == b'=' {
            0
        } else {
            decode_base64_byte(chunk[3])?
        };
        output.push((a << 2) | (b >> 4));
        if chunk[2] != b'=' {
            output.push((b << 4) | (c >> 2));
        }
        if chunk[3] != b'=' {
            output.push((c << 6) | d);
        }
    }
    Ok(output)
}

fn decode_base64_byte(value: u8) -> Result<u8, ()> {
    match value {
        b'A'..=b'Z' => Ok(value - b'A'),
        b'a'..=b'z' => Ok(value - b'a' + 26),
        b'0'..=b'9' => Ok(value - b'0' + 52),
        b'+' => Ok(62),
        b'/' => Ok(63),
        _ => Err(()),
    }
}
