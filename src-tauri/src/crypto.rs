//! Cryptographic primitives used by the local vault.
//!
//! The format identifier is intentionally public.  It is authenticated as AEAD associated data,
//! but it is never used as a password, key, seed, or other secret material.

use argon2::{Algorithm, Argon2, Params, Version};
use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD};
use chacha20poly1305::{
    XChaCha20Poly1305, XNonce,
    aead::{Aead, KeyInit, Payload},
};
use serde::{Deserialize, Serialize};
use zeroize::Zeroizing;

use crate::error::{Result, VaultError};

pub const VAULT_FORMAT: &str = "neko233-v1";
pub const DEK_BYTES: usize = 32;
pub const KDF_SALT_BYTES: usize = 16;
pub const XCHACHA_NONCE_BYTES: usize = 24;
pub const MIN_PASSWORD_CHARS: usize = 12;
/// The persisted KDF metadata is attacker-controlled when a vault is restored from disk or a
/// cloud backup.  Keep an explicit upper bound below the default memory budget of a desktop
/// process so a malformed envelope cannot turn a password prompt into a multi-gigabyte
/// allocation.
pub const MIN_KDF_MEMORY_KIB: u32 = 32 * 1024;
pub const MAX_KDF_MEMORY_KIB: u32 = 128 * 1024;
pub const MAX_KDF_ITERATIONS: u32 = 6;
pub const MAX_KDF_PARALLELISM: u32 = 4;

/// Parameters travel with the encrypted vault so that a password can be re-derived on another
/// device.  Bounds are checked before allocating Argon2 memory.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Argon2idConfig {
    pub algorithm: String,
    pub version: u32,
    pub memory_kib: u32,
    pub iterations: u32,
    pub parallelism: u32,
    pub salt_b64: String,
}

impl Argon2idConfig {
    pub fn secure_default(salt: [u8; KDF_SALT_BYTES]) -> Self {
        Self {
            algorithm: "argon2id".to_owned(),
            version: 0x13,
            // 64 MiB / 3 passes is a deliberately expensive desktop-vault default.
            memory_kib: 65_536,
            iterations: 3,
            parallelism: 1,
            salt_b64: URL_SAFE_NO_PAD.encode(salt),
        }
    }

    pub fn validate(&self) -> Result<[u8; KDF_SALT_BYTES]> {
        if self.algorithm != "argon2id" || self.version != 0x13 {
            return Err(VaultError::UnsupportedFormat);
        }
        // Prevent a manipulated vault file from requesting unreasonable memory/CPU before
        // Argon2 allocates its working area.  These bounds also apply to remotely downloaded
        // encrypted backups before the user is asked for a password.
        if self.parallelism == 0
            || self.parallelism > MAX_KDF_PARALLELISM
            || self.iterations == 0
            || self.iterations > MAX_KDF_ITERATIONS
            || self.memory_kib < MIN_KDF_MEMORY_KIB
            || self.memory_kib < self.parallelism.saturating_mul(8)
            || self.memory_kib > MAX_KDF_MEMORY_KIB
        {
            return Err(VaultError::Malformed);
        }

        let salt = URL_SAFE_NO_PAD
            .decode(&self.salt_b64)
            .map_err(|_| VaultError::Malformed)?;
        salt.try_into().map_err(|_| VaultError::Malformed)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EncryptedPayload {
    /// A fresh 192-bit nonce for this XChaCha20-Poly1305 ciphertext.
    pub nonce_b64: String,
    /// Includes the 128-bit Poly1305 authentication tag.
    pub ciphertext_b64: String,
}

impl EncryptedPayload {
    fn decode_nonce(&self) -> Result<[u8; XCHACHA_NONCE_BYTES]> {
        let nonce = URL_SAFE_NO_PAD
            .decode(&self.nonce_b64)
            .map_err(|_| VaultError::Malformed)?;
        nonce.try_into().map_err(|_| VaultError::Malformed)
    }

    fn decode_ciphertext(&self) -> Result<Vec<u8>> {
        URL_SAFE_NO_PAD
            .decode(&self.ciphertext_b64)
            .map_err(|_| VaultError::Malformed)
    }

    pub fn validate_encoding(&self) -> Result<()> {
        self.decode_nonce()?;
        // XChaCha20-Poly1305 ciphertext always carries at least a 128-bit authentication tag.
        if self.decode_ciphertext()?.len() < 16 {
            return Err(VaultError::Malformed);
        }
        Ok(())
    }
}

pub fn validate_password(password: &str) -> Result<()> {
    if password.chars().count() < MIN_PASSWORD_CHARS {
        return Err(VaultError::WeakPassword(MIN_PASSWORD_CHARS));
    }
    Ok(())
}

pub fn random_dek() -> Result<Zeroizing<[u8; DEK_BYTES]>> {
    let mut dek = [0_u8; DEK_BYTES];
    getrandom::fill(&mut dek).map_err(|_| VaultError::Random)?;
    Ok(Zeroizing::new(dek))
}

pub fn fresh_kdf_config() -> Result<Argon2idConfig> {
    let mut salt = [0_u8; KDF_SALT_BYTES];
    getrandom::fill(&mut salt).map_err(|_| VaultError::Random)?;
    Ok(Argon2idConfig::secure_default(salt))
}

pub fn derive_kek(password: &str, config: &Argon2idConfig) -> Result<Zeroizing<[u8; DEK_BYTES]>> {
    let salt = config.validate()?;
    let params = Params::new(
        config.memory_kib,
        config.iterations,
        config.parallelism,
        Some(DEK_BYTES),
    )
    .map_err(|_| VaultError::Malformed)?;
    let argon2 = Argon2::new(Algorithm::Argon2id, Version::V0x13, params);
    let mut kek = Zeroizing::new([0_u8; DEK_BYTES]);
    argon2
        .hash_password_into(password.as_bytes(), &salt, kek.as_mut())
        .map_err(|_| VaultError::EncryptionFailed)?;
    Ok(kek)
}

pub fn wrap_dek(dek: &[u8; DEK_BYTES], kek: &[u8; DEK_BYTES]) -> Result<EncryptedPayload> {
    encrypt(kek, dek, &aad("wrapped-dek"))
}

pub fn unwrap_dek(
    wrapped_dek: &EncryptedPayload,
    kek: &[u8; DEK_BYTES],
) -> Result<Zeroizing<[u8; DEK_BYTES]>> {
    let plain = decrypt(kek, wrapped_dek, &aad("wrapped-dek"))?;
    let dek: [u8; DEK_BYTES] = plain.try_into().map_err(|_| VaultError::UnlockFailed)?;
    Ok(Zeroizing::new(dek))
}

pub fn encrypt(
    key: &[u8; DEK_BYTES],
    plaintext: &[u8],
    associated_data: &[u8],
) -> Result<EncryptedPayload> {
    let cipher =
        XChaCha20Poly1305::new_from_slice(key).map_err(|_| VaultError::EncryptionFailed)?;
    let mut nonce = [0_u8; XCHACHA_NONCE_BYTES];
    getrandom::fill(&mut nonce).map_err(|_| VaultError::Random)?;
    let ciphertext = cipher
        .encrypt(
            XNonce::from_slice(&nonce),
            Payload {
                msg: plaintext,
                aad: associated_data,
            },
        )
        .map_err(|_| VaultError::EncryptionFailed)?;

    Ok(EncryptedPayload {
        nonce_b64: URL_SAFE_NO_PAD.encode(nonce),
        ciphertext_b64: URL_SAFE_NO_PAD.encode(ciphertext),
    })
}

/// Authentication failures intentionally collapse wrong passwords and tampered ciphertext into
/// the same public error, avoiding an oracle over vault contents.
pub fn decrypt(
    key: &[u8; DEK_BYTES],
    payload: &EncryptedPayload,
    associated_data: &[u8],
) -> Result<Vec<u8>> {
    let nonce = payload.decode_nonce()?;
    let ciphertext = payload.decode_ciphertext()?;
    let cipher = XChaCha20Poly1305::new_from_slice(key).map_err(|_| VaultError::UnlockFailed)?;
    cipher
        .decrypt(
            XNonce::from_slice(&nonce),
            Payload {
                msg: &ciphertext,
                aad: associated_data,
            },
        )
        .map_err(|_| VaultError::UnlockFailed)
}

pub fn aad(section: &str) -> Vec<u8> {
    format!("{VAULT_FORMAT}:{section}").into_bytes()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn aead_rejects_modified_associated_data() {
        let key = random_dek().expect("random key");
        let payload = encrypt(&key, b"private", b"purpose-a").expect("encrypt");
        assert!(matches!(
            decrypt(&key, &payload, b"purpose-b"),
            Err(VaultError::UnlockFailed)
        ));
    }

    #[test]
    fn kdf_metadata_rejects_unsafe_values() {
        let mut config = fresh_kdf_config().expect("config");
        config.memory_kib = MAX_KDF_MEMORY_KIB + 1;
        assert!(matches!(config.validate(), Err(VaultError::Malformed)));

        config.memory_kib = MIN_KDF_MEMORY_KIB - 1;
        assert!(matches!(config.validate(), Err(VaultError::Malformed)));

        config.memory_kib = MIN_KDF_MEMORY_KIB;
        config.iterations = MAX_KDF_ITERATIONS + 1;
        assert!(matches!(config.validate(), Err(VaultError::Malformed)));

        config.iterations = 1;
        config.parallelism = MAX_KDF_PARALLELISM + 1;
        assert!(matches!(config.validate(), Err(VaultError::Malformed)));
    }

    #[test]
    fn format_label_is_not_a_secret() {
        assert_eq!(VAULT_FORMAT, "neko233-v1");
        assert_ne!(VAULT_FORMAT.len(), DEK_BYTES);
    }
}
