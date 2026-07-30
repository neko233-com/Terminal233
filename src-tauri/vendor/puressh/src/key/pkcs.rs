//! Non-OpenSSH private-key formats: PKCS#1, SEC1, and PKCS#8 PEM.
//!
//! OpenSSH's own `-----BEGIN OPENSSH PRIVATE KEY-----` container is handled by
//! [`super::PrivateKey::parse_openssh_pem`]. This module adds the *other* PEM
//! encodings commonly produced by `openssl`, older `ssh-keygen -m PEM`, and
//! various language ecosystems, so a user can point an identity file at a key
//! that was not minted by recent OpenSSH:
//!
//! - `-----BEGIN RSA PRIVATE KEY-----`  — PKCS#1 `RSAPrivateKey` (RFC 8017).
//! - `-----BEGIN EC PRIVATE KEY-----`   — SEC1 `ECPrivateKey` (RFC 5915).
//! - `-----BEGIN PRIVATE KEY-----`      — PKCS#8 `PrivateKeyInfo` (RFC 5958),
//!   dispatched by algorithm OID to RSA / ECDSA (nistp256/384/521) / Ed25519.
//!
//! Encrypted variants (`-----BEGIN ENCRYPTED PRIVATE KEY-----`, and the legacy
//! `Proc-Type: 4,ENCRYPTED` / `DEK-Info` headers on traditional PEM) are
//! recognised but rejected with a clear [`Error::Unsupported`] rather than
//! mis-parsed — passphrase-protected keys in these formats are out of scope for
//! now (the OpenSSH container remains the encrypted-key path).
//!
//! DER component extraction is done with [`purecrypto::der::Reader`]; the
//! resulting big-endian integers map directly onto the SSH `mpint` convention
//! already used by [`super::PrivateKey`] (both are minimal big-endian with a
//! `0x00` sign byte when the high bit is set), and [`write_mpint`] normalises on
//! serialisation regardless. ECDSA/Ed25519 public parts are *derived* from the
//! private scalar/seed via `purecrypto`, so a missing or inconsistent embedded
//! public key cannot smuggle in a mismatched point.
//!
//! [`write_mpint`]: crate::format::write_mpint

use alloc::string::String;
use alloc::vec::Vec;

use purecrypto::der::Reader as DerReader;
use purecrypto::der::pem_decode;
use purecrypto::ec::{BoxedEcdsaPrivateKey, CurveId, Ed25519PrivateKey};
use zeroize::Zeroizing;

use super::PrivateKey;
use crate::error::{Error, Result};

// --- Object identifiers (encoded body bytes, i.e. the OID value without the
// outer tag/length). Compared by slice equality against `Reader::read_oid`. ---

/// `rsaEncryption` — 1.2.840.113549.1.1.1.
const OID_RSA: &[u8] = &[0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01];
/// `id-ecPublicKey` — 1.2.840.10045.2.1.
const OID_EC_PUBLICKEY: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01];
/// `id-Ed25519` — 1.3.101.112.
const OID_ED25519: &[u8] = &[0x2b, 0x65, 0x70];
/// `secp256r1` (prime256v1) — 1.2.840.10045.3.1.7.
const OID_P256: &[u8] = &[0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07];
/// `secp384r1` — 1.3.132.0.34.
const OID_P384: &[u8] = &[0x2b, 0x81, 0x04, 0x00, 0x22];
/// `secp521r1` — 1.3.132.0.35.
const OID_P521: &[u8] = &[0x2b, 0x81, 0x04, 0x00, 0x23];

/// EXPLICIT context tag `[0]` (constructed) — SEC1 `parameters`.
const TAG_CTX0: u8 = 0xa0;
/// EXPLICIT context tag `[1]` (constructed) — SEC1 `publicKey`.
const TAG_CTX1: u8 = 0xa1;
/// Universal `OBJECT IDENTIFIER` tag.
const TAG_OID: u8 = 0x06;
/// Universal `NULL` tag.
const TAG_NULL: u8 = 0x05;

fn der_err(_e: purecrypto::der::Error) -> Error {
    Error::Format("der: malformed private key structure")
}
impl PrivateKey {
    /// Parse a PEM private key in any supported container, auto-detecting the
    /// format from the `-----BEGIN <label>-----` marker.
    ///
    /// Supported labels:
    /// - `OPENSSH PRIVATE KEY` — delegates to [`Self::parse_openssh_pem`]
    ///   (the only container that currently supports a `passphrase`).
    /// - `RSA PRIVATE KEY` — PKCS#1.
    /// - `EC PRIVATE KEY` — SEC1.
    /// - `PRIVATE KEY` — PKCS#8 (RSA / ECDSA nistp256/384/521 / Ed25519).
    ///
    /// `passphrase` is honoured only for the OpenSSH container. Encrypted
    /// PKCS#8 (`ENCRYPTED PRIVATE KEY`) and legacy `DEK-Info` traditional PEM
    /// are rejected with [`Error::Unsupported`].
    ///
    /// This is the entry point identity-file loaders should prefer over
    /// [`Self::parse_openssh_pem`] when the on-disk format is not known ahead of
    /// time.
    pub fn parse_pem(pem: &str, passphrase: Option<&[u8]>) -> Result<Self> {
        let label = detect_pem_label(pem)?;
        match label {
            "OPENSSH PRIVATE KEY" => Self::parse_openssh_pem(pem, passphrase),
            "ENCRYPTED PRIVATE KEY" => Err(Error::Unsupported(
                "encrypted PKCS#8 private keys are not supported (convert with `ssh-keygen -p`)",
            )),
            "RSA PRIVATE KEY" => {
                reject_if_dek_encrypted(pem)?;
                let der = Zeroizing::new(pem_decode(pem, label).map_err(der_err)?);
                parse_pkcs1_rsa_der(&der)
            }
            "EC PRIVATE KEY" => {
                reject_if_dek_encrypted(pem)?;
                let der = Zeroizing::new(pem_decode(pem, label).map_err(der_err)?);
                parse_sec1_ec_der(&der, None)
            }
            "PRIVATE KEY" => {
                let der = Zeroizing::new(pem_decode(pem, label).map_err(der_err)?);
                parse_pkcs8_der(&der)
            }
            _ => Err(Error::Unsupported("unrecognised PEM private-key type")),
        }
    }
}

/// Extract the label from the first `-----BEGIN <label>-----` marker.
fn detect_pem_label(pem: &str) -> Result<&str> {
    const BEGIN: &str = "-----BEGIN ";
    let start = pem
        .find(BEGIN)
        .ok_or(Error::Format("pem: missing BEGIN marker"))?
        + BEGIN.len();
    let rest = &pem[start..];
    let end = rest
        .find("-----")
        .ok_or(Error::Format("pem: malformed BEGIN marker"))?;
    Ok(rest[..end].trim())
}

/// Reject traditional PEM that carries an encrypted body (`Proc-Type` /
/// `DEK-Info` headers, RFC 1421 / OpenSSL legacy encryption).
fn reject_if_dek_encrypted(pem: &str) -> Result<()> {
    if pem.contains("DEK-Info") || pem.contains("Proc-Type") {
        return Err(Error::Unsupported(
            "encrypted traditional PEM (DEK-Info) is not supported (convert with `ssh-keygen -p`)",
        ));
    }
    Ok(())
}

/// PKCS#1 `RSAPrivateKey` (RFC 8017 §A.1.2):
/// `SEQUENCE { version, n, e, d, p, q, dP, dQ, qInv }`.
fn parse_pkcs1_rsa_der(der: &[u8]) -> Result<PrivateKey> {
    let mut top = DerReader::new(der);
    let mut seq = top.read_sequence().map_err(der_err)?;
    let version = seq.read_unsigned_integer_bytes().map_err(der_err)?;
    // version 0 = two-prime; multi-prime (1) carries extra fields we don't model.
    if version != [0u8] {
        return Err(Error::Unsupported("rsa: only two-prime keys are supported"));
    }
    let n = seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec();
    let e = seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec();
    let mut d = Zeroizing::new(seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec());
    let mut p = Zeroizing::new(seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec());
    let mut q = Zeroizing::new(seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec());
    let _dp = seq.read_unsigned_integer_bytes().map_err(der_err)?;
    let _dq = seq.read_unsigned_integer_bytes().map_err(der_err)?;
    let mut iqmp = Zeroizing::new(seq.read_unsigned_integer_bytes().map_err(der_err)?.to_vec());
    Ok(PrivateKey::Rsa {
        n,
        e,
        d: core::mem::take(&mut *d),
        p: core::mem::take(&mut *p),
        q: core::mem::take(&mut *q),
        iqmp: core::mem::take(&mut *iqmp),
        comment: String::new(),
    })
}

/// SEC1 `ECPrivateKey` (RFC 5915 §3):
/// `SEQUENCE { version(1), privateKey OCTET STRING, [0] parameters OPTIONAL,
///  [1] publicKey OPTIONAL }`.
///
/// `curve_hint` supplies the curve OID when this structure is nested inside a
/// PKCS#8 wrapper (which carries the curve in its `AlgorithmIdentifier` and may
/// omit the inner `[0] parameters`).
fn parse_sec1_ec_der(der: &[u8], curve_hint: Option<&[u8]>) -> Result<PrivateKey> {
    let mut top = DerReader::new(der);
    let mut seq = top.read_sequence().map_err(der_err)?;
    let version = seq.read_unsigned_integer_bytes().map_err(der_err)?;
    if version != [0x01] {
        return Err(Error::Format("ec: unexpected SEC1 version"));
    }
    let d_raw = seq.read_octet_string().map_err(der_err)?;

    // Optional [0] parameters (named-curve OID) and [1] publicKey. We prefer an
    // inline [0] OID, falling back to the PKCS#8 hint. The [1] public key is
    // ignored: we derive the point from the scalar instead.
    let mut curve_oid: Option<Vec<u8>> = curve_hint.map(|o| o.to_vec());
    while !seq.is_empty() {
        let (tag, content) = seq.read_any().map_err(der_err)?;
        if tag == TAG_CTX0 {
            // EXPLICIT [0] wraps ECParameters; the namedCurve choice is an OID.
            let mut inner = DerReader::new(content);
            curve_oid = Some(inner.read_oid().map_err(der_err)?.to_vec());
        } else if tag == TAG_CTX1 {
            // publicKey BIT STRING — derived, not trusted.
        }
    }

    let oid = curve_oid.ok_or(Error::Format("ec: missing curve identifier"))?;
    let (curve, ctor) = curve_from_oid(&oid)?;
    let sk = BoxedEcdsaPrivateKey::from_bytes(curve, d_raw)
        .map_err(|_| Error::Crypto("ec: private scalar out of range"))?;
    let point = sk.public_key().to_sec1();
    Ok(ctor(trim_leading_zeros(d_raw).to_vec(), point))
}

/// PKCS#8 `PrivateKeyInfo` (RFC 5958 §2):
/// `SEQUENCE { version, AlgorithmIdentifier { OID, params OPTIONAL },
///  privateKey OCTET STRING }`.
fn parse_pkcs8_der(der: &[u8]) -> Result<PrivateKey> {
    let mut top = DerReader::new(der);
    let mut seq = top.read_sequence().map_err(der_err)?;
    let _version = seq.read_unsigned_integer_bytes().map_err(der_err)?;

    let mut alg = seq.read_sequence().map_err(der_err)?;
    let alg_oid = alg.read_oid().map_err(der_err)?;
    // Algorithm parameters: NULL for RSA, the curve OID for EC, absent for
    // Ed25519.
    let params_oid: Option<&[u8]> = match alg.peek_tag() {
        Some(TAG_OID) => Some(alg.read_oid().map_err(der_err)?),
        Some(TAG_NULL) => {
            alg.read_null().map_err(der_err)?;
            None
        }
        _ => None,
    };

    let priv_key = seq.read_octet_string().map_err(der_err)?;

    if alg_oid == OID_RSA {
        parse_pkcs1_rsa_der(priv_key)
    } else if alg_oid == OID_EC_PUBLICKEY {
        parse_sec1_ec_der(priv_key, params_oid)
    } else if alg_oid == OID_ED25519 {
        parse_pkcs8_ed25519(priv_key)
    } else {
        Err(Error::Unsupported("pkcs8: unsupported key algorithm"))
    }
}

/// The Ed25519 PKCS#8 `privateKey` field is a `CurvePrivateKey` — itself a DER
/// `OCTET STRING` wrapping the 32-byte seed (RFC 8410 §7).
fn parse_pkcs8_ed25519(inner: &[u8]) -> Result<PrivateKey> {
    let mut r = DerReader::new(inner);
    let seed_bytes = r.read_octet_string().map_err(der_err)?;
    if seed_bytes.len() != 32 {
        return Err(Error::Format("ed25519: seed length"));
    }
    let mut seed = Zeroizing::new([0u8; 32]);
    seed.copy_from_slice(seed_bytes);
    let public = Ed25519PrivateKey::from_bytes(*seed).public_key().to_bytes();
    Ok(PrivateKey::Ed25519 {
        seed: *seed,
        public,
        comment: String::new(),
    })
}

type EcdsaCtor = fn(Vec<u8>, Vec<u8>) -> PrivateKey;

/// Map a named-curve OID to its `purecrypto` curve and the matching
/// [`PrivateKey`] constructor.
fn curve_from_oid(oid: &[u8]) -> Result<(CurveId, EcdsaCtor)> {
    if oid == OID_P256 {
        Ok((CurveId::P256, |d, point| PrivateKey::EcdsaP256 {
            d,
            point,
            comment: String::new(),
        }))
    } else if oid == OID_P384 {
        Ok((CurveId::P384, |d, point| PrivateKey::EcdsaP384 {
            d,
            point,
            comment: String::new(),
        }))
    } else if oid == OID_P521 {
        Ok((CurveId::P521, |d, point| PrivateKey::EcdsaP521 {
            d,
            point,
            comment: String::new(),
        }))
    } else {
        Err(Error::Unsupported("ec: unsupported curve"))
    }
}

fn trim_leading_zeros(b: &[u8]) -> &[u8] {
    let mut i = 0;
    while i + 1 < b.len() && b[i] == 0 {
        i += 1;
    }
    &b[i..]
}
