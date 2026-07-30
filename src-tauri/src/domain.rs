//! Minimal SSH domain model shared by the native Rust IDE services.
//!
//! There is deliberately no persistent credential store, sync layer, external-process OpenSSH,
//! PuTTY, plink, or Windows Terminal plan in this module.

use std::{fmt, time::SystemTime};

use base64::{Engine as _, engine::general_purpose::STANDARD_NO_PAD};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use uuid::Uuid;
use zeroize::Zeroize;

use crate::error::{NekoError, Result};

pub struct StoredSecret(String);

impl StoredSecret {
    pub fn new(value: String) -> Self {
        Self(value)
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    pub(crate) fn with_exposed<R>(&self, use_secret: impl FnOnce(&str) -> R) -> R {
        use_secret(&self.0)
    }
}

impl Clone for StoredSecret {
    fn clone(&self) -> Self {
        Self(self.0.clone())
    }
}

impl fmt::Debug for StoredSecret {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("StoredSecret([REDACTED])")
    }
}

impl Drop for StoredSecret {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

#[derive(Clone, Debug)]
pub enum StoredAuthentication {
    Password {
        password: StoredSecret,
    },
    PrivateKey {
        private_key_pem: StoredSecret,
        passphrase: Option<StoredSecret>,
    },
}

impl StoredAuthentication {
    fn from_input(input: ProfileAuthenticationInput) -> Result<Self> {
        match input {
            ProfileAuthenticationInput::Password { password } => {
                validate_secret(&password, "password cannot be empty")?;
                Ok(Self::Password {
                    password: StoredSecret::new(password),
                })
            }
            ProfileAuthenticationInput::PrivateKey {
                private_key_pem,
                passphrase,
            } => {
                validate_secret(&private_key_pem, "private key cannot be empty")?;
                if let Some(value) = &passphrase {
                    validate_secret(value, "private key passphrase cannot be empty")?;
                }
                Ok(Self::PrivateKey {
                    private_key_pem: StoredSecret::new(private_key_pem),
                    passphrase: passphrase.map(StoredSecret::new),
                })
            }
        }
    }
}

#[derive(Clone, PartialEq, Eq, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ProfileAuthenticationInput {
    Password {
        password: String,
    },
    PrivateKey {
        #[serde(rename = "privateKeyPem")]
        private_key_pem: String,
        passphrase: Option<String>,
    },
}

impl fmt::Debug for ProfileAuthenticationInput {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Password { .. } => {
                formatter.write_str("ProfileAuthenticationInput::Password([REDACTED])")
            }
            Self::PrivateKey { .. } => {
                formatter.write_str("ProfileAuthenticationInput::PrivateKey([REDACTED])")
            }
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct TerminalPreferences {
    #[serde(default = "default_terminal_columns")]
    pub columns: u16,
    #[serde(default = "default_terminal_rows")]
    pub rows: u16,
    #[serde(default)]
    pub command: Option<String>,
}

const fn default_terminal_columns() -> u16 {
    120
}

const fn default_terminal_rows() -> u16 {
    32
}

impl Default for TerminalPreferences {
    fn default() -> Self {
        Self {
            columns: default_terminal_columns(),
            rows: default_terminal_rows(),
            command: None,
        }
    }
}

#[derive(Clone, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SshProfileInput {
    pub label: String,
    pub host: String,
    #[serde(default = "default_ssh_port")]
    pub port: u16,
    pub username: String,
    pub authentication: ProfileAuthenticationInput,
    #[serde(default)]
    pub tags: Vec<String>,
    #[serde(default)]
    pub terminal: TerminalPreferences,
}

impl fmt::Debug for SshProfileInput {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("SshProfileInput")
            .field("label", &self.label)
            .field("host", &self.host)
            .field("port", &self.port)
            .field("username", &self.username)
            .field("authentication", &self.authentication)
            .field("tags", &self.tags)
            .field("terminal", &self.terminal)
            .finish()
    }
}

const fn default_ssh_port() -> u16 {
    22
}

#[derive(Clone, Debug)]
pub struct SshProfile {
    pub id: Uuid,
    pub label: String,
    pub host: String,
    pub port: u16,
    pub username: String,
    pub authentication: StoredAuthentication,
    pub tags: Vec<String>,
    pub terminal: TerminalPreferences,
    pub created_at_unix_ms: u64,
    pub updated_at_unix_ms: u64,
}

impl SshProfile {
    pub fn from_input(input: SshProfileInput, now_unix_ms: u64) -> Result<Self> {
        validate_label(&input.label)?;
        validate_host(&input.host)?;
        validate_username(&input.username)?;
        validate_port(input.port)?;
        validate_tags(&input.tags)?;
        validate_terminal(&input.terminal)?;
        Ok(Self {
            id: Uuid::new_v4(),
            label: input.label,
            host: input.host,
            port: input.port,
            username: input.username,
            authentication: StoredAuthentication::from_input(input.authentication)?,
            tags: input.tags,
            terminal: input.terminal,
            created_at_unix_ms: now_unix_ms,
            updated_at_unix_ms: now_unix_ms,
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct KnownHostRecord {
    host: String,
    port: u16,
    key_algorithm: String,
    fingerprint: String,
    trusted_at_unix_ms: u64,
}

impl KnownHostRecord {
    pub fn from_presented(
        host: String,
        port: u16,
        key_algorithm: String,
        public_key: &[u8],
        trusted_at_unix_ms: u64,
    ) -> Result<Self> {
        validate_host(&host)?;
        validate_port(port)?;
        validate_key_algorithm(&key_algorithm)?;
        validate_public_key(public_key)?;
        Ok(Self {
            host,
            port,
            key_algorithm,
            fingerprint: host_key_fingerprint(public_key),
            trusted_at_unix_ms,
        })
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct KnownHosts {
    records: Vec<KnownHostRecord>,
}

impl KnownHosts {
    pub fn verify(
        &self,
        host: &str,
        port: u16,
        key_algorithm: &str,
        public_key: &[u8],
    ) -> HostKeyVerification {
        let fingerprint = host_key_fingerprint(public_key);
        let endpoint: Vec<_> = self
            .records
            .iter()
            .filter(|record| record.host == host && record.port == port)
            .collect();
        if endpoint.is_empty() {
            return HostKeyVerification::Unknown { fingerprint };
        }
        if endpoint.iter().any(|record| {
            record.key_algorithm == key_algorithm && record.fingerprint == fingerprint
        }) {
            return HostKeyVerification::Trusted { fingerprint };
        }
        let mut expected_fingerprints: Vec<_> = endpoint
            .into_iter()
            .map(|record| record.fingerprint.clone())
            .collect();
        expected_fingerprints.sort();
        expected_fingerprints.dedup();
        HostKeyVerification::Mismatch {
            fingerprint,
            expected_fingerprints,
        }
    }

    pub fn trust(&mut self, record: KnownHostRecord) {
        self.records.retain(|existing| {
            !(existing.host == record.host
                && existing.port == record.port
                && existing.key_algorithm == record.key_algorithm)
        });
        self.records.push(record);
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum HostKeyVerification {
    Trusted {
        fingerprint: String,
    },
    Unknown {
        fingerprint: String,
    },
    Mismatch {
        fingerprint: String,
        expected_fingerprints: Vec<String>,
    },
}

pub fn host_key_fingerprint(public_key: &[u8]) -> String {
    let digest = Sha256::digest(public_key);
    format!("SHA256:{}", STANDARD_NO_PAD.encode(digest))
}

fn validate_label(value: &str) -> Result<()> {
    validate_text(value, 128, "label is invalid")
}

fn validate_host(value: &str) -> Result<()> {
    validate_text(value, 253, "host is invalid")?;
    if value.contains(['/', '\\', '@'])
        || value.chars().any(char::is_whitespace)
        || value.starts_with('-')
    {
        return Err(NekoError::InvalidProfile("host is invalid"));
    }
    Ok(())
}

fn validate_username(value: &str) -> Result<()> {
    validate_text(value, 128, "username is invalid")?;
    if value.chars().any(char::is_whitespace) || value.contains(['@', '/', '\\']) {
        return Err(NekoError::InvalidProfile("username is invalid"));
    }
    Ok(())
}

fn validate_port(port: u16) -> Result<()> {
    if port == 0 {
        Err(NekoError::InvalidProfile("port is invalid"))
    } else {
        Ok(())
    }
}

fn validate_secret(value: &str, message: &'static str) -> Result<()> {
    if value.is_empty() || value.len() > 64 * 1024 || value.as_bytes().contains(&0) {
        Err(NekoError::InvalidProfile(message))
    } else {
        Ok(())
    }
}

fn validate_tags(tags: &[String]) -> Result<()> {
    if tags.len() > 32 {
        return Err(NekoError::InvalidProfile("at most 32 tags are allowed"));
    }
    for tag in tags {
        validate_text(tag, 64, "tag is invalid")?;
    }
    Ok(())
}

fn validate_terminal(terminal: &TerminalPreferences) -> Result<()> {
    if !(20..=500).contains(&terminal.columns) || !(5..=300).contains(&terminal.rows) {
        return Err(NekoError::InvalidProfile("terminal dimensions are invalid"));
    }
    if let Some(command) = &terminal.command {
        validate_text(command, 32 * 1024, "terminal command is invalid")?;
    }
    Ok(())
}

fn validate_key_algorithm(value: &str) -> Result<()> {
    validate_text(value, 128, "host key algorithm is invalid")
}

fn validate_public_key(value: &[u8]) -> Result<()> {
    if value.is_empty() || value.len() > 64 * 1024 {
        Err(NekoError::InvalidProfile("presented host key is invalid"))
    } else {
        Ok(())
    }
}

fn validate_text(value: &str, maximum_bytes: usize, message: &'static str) -> Result<()> {
    if value.is_empty()
        || value.len() > maximum_bytes
        || value.as_bytes().contains(&0)
        || value.chars().any(char::is_control)
    {
        Err(NekoError::InvalidProfile(message))
    } else {
        Ok(())
    }
}

#[allow(dead_code)]
fn now_unix_ms() -> u64 {
    SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .try_into()
        .unwrap_or(u64::MAX)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn input() -> SshProfileInput {
        SshProfileInput {
            label: "test".to_owned(),
            host: "example.test".to_owned(),
            port: 22,
            username: "root".to_owned(),
            authentication: ProfileAuthenticationInput::Password {
                password: "temporary".to_owned(),
            },
            tags: Vec::new(),
            terminal: TerminalPreferences::default(),
        }
    }

    #[test]
    fn profile_validation_accepts_an_ephemeral_password_connection() {
        let profile = SshProfile::from_input(input(), now_unix_ms()).expect("valid profile");
        assert_eq!(profile.host, "example.test");
        assert!(matches!(
            profile.authentication,
            StoredAuthentication::Password { .. }
        ));
    }

    #[test]
    fn profile_json_rejects_process_external_ssh_agent_authentication() {
        let error = serde_json::from_value::<SshProfileInput>(serde_json::json!({
            "label": "test",
            "host": "example.test",
            "port": 22,
            "username": "root",
            "authentication": { "kind": "agent" },
            "tags": [],
            "terminal": { "columns": 120, "rows": 32, "command": null }
        }))
        .expect_err("SSH Agent must not cross the native IPC boundary");
        assert!(
            error.to_string().contains("unknown variant `agent`"),
            "{error}"
        );
    }

    #[test]
    fn known_hosts_requires_confirmation_and_detects_algorithm_changes() {
        let mut known_hosts = KnownHosts::default();
        let first = b"first-public-key";
        assert!(matches!(
            known_hosts.verify("example.test", 22, "ssh-ed25519", first),
            HostKeyVerification::Unknown { .. }
        ));
        known_hosts.trust(
            KnownHostRecord::from_presented(
                "example.test".to_owned(),
                22,
                "ssh-ed25519".to_owned(),
                first,
                now_unix_ms(),
            )
            .expect("record"),
        );
        assert!(matches!(
            known_hosts.verify("example.test", 22, "ssh-ed25519", first),
            HostKeyVerification::Trusted { .. }
        ));
        assert!(matches!(
            known_hosts.verify("example.test", 22, "rsa-sha2-512", b"other-key"),
            HostKeyVerification::Mismatch { .. }
        ));
    }

    #[test]
    fn debug_never_reveals_credentials() {
        let input = input();
        let rendered = format!("{input:?}");
        assert!(rendered.contains("[REDACTED]"));
        assert!(!rendered.contains("temporary"));
    }
}
