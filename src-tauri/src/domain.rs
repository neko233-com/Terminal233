//! SSH and sync domain types.
//!
//! These types intentionally separate public summaries and execution plans from sensitive data.
//! Passwords, private keys, and sync tokens are serialized only inside the encrypted vault.

use std::{
    fmt,
    net::IpAddr,
    path::{Path, PathBuf},
    process::{Child, Command},
};

#[cfg(not(windows))]
use std::process::Stdio;

use base64::{Engine as _, engine::general_purpose::STANDARD_NO_PAD};
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use sha2::{Digest, Sha256};
use url::Url;
use uuid::Uuid;
use zeroize::Zeroize;

use crate::error::{Result, VaultError};

/// String material that is encrypted at rest by the vault.  It is deliberately redacted in
/// `Debug`, and zeroized when its owning value is dropped.
pub struct StoredSecret(String);

impl StoredSecret {
    pub fn new(value: String) -> Self {
        Self(value)
    }

    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }

    /// Gives trusted, local transport code a short-lived view of the secret.  This is crate
    /// private so IPC summaries and execution plans cannot expose credential material.
    pub(crate) fn with_exposed<R>(&self, use_secret: impl FnOnce(&str) -> R) -> R {
        use_secret(&self.0)
    }
}

impl Clone for StoredSecret {
    fn clone(&self) -> Self {
        Self(self.0.clone())
    }
}

impl PartialEq for StoredSecret {
    fn eq(&self, other: &Self) -> bool {
        self.0 == other.0
    }
}

impl Eq for StoredSecret {}

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

impl Serialize for StoredSecret {
    fn serialize<S>(&self, serializer: S) -> std::result::Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        serializer.serialize_str(&self.0)
    }
}

impl<'de> Deserialize<'de> for StoredSecret {
    fn deserialize<D>(deserializer: D) -> std::result::Result<Self, D::Error>
    where
        D: Deserializer<'de>,
    {
        String::deserialize(deserializer).map(Self::new)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthenticationKind {
    Agent,
    Password,
    PrivateKey,
    PrivateKeyFile,
}

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum StoredAuthentication {
    Agent,
    Password {
        password: StoredSecret,
    },
    PrivateKey {
        private_key_pem: StoredSecret,
        passphrase: Option<StoredSecret>,
    },
    PrivateKeyFile {
        private_key_path: String,
    },
}

impl fmt::Debug for StoredAuthentication {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Agent => formatter.write_str("StoredAuthentication::Agent"),
            Self::Password { .. } => {
                formatter.write_str("StoredAuthentication::Password([REDACTED])")
            }
            Self::PrivateKey { .. } => {
                formatter.write_str("StoredAuthentication::PrivateKey([REDACTED])")
            }
            Self::PrivateKeyFile { .. } => {
                formatter.write_str("StoredAuthentication::PrivateKeyFile([REDACTED])")
            }
        }
    }
}

impl StoredAuthentication {
    pub fn kind(&self) -> AuthenticationKind {
        match self {
            Self::Agent => AuthenticationKind::Agent,
            Self::Password { .. } => AuthenticationKind::Password,
            Self::PrivateKey { .. } => AuthenticationKind::PrivateKey,
            Self::PrivateKeyFile { .. } => AuthenticationKind::PrivateKeyFile,
        }
    }

    fn from_input(input: ProfileAuthenticationInput) -> Result<Self> {
        match input {
            ProfileAuthenticationInput::Agent => Ok(Self::Agent),
            ProfileAuthenticationInput::Password { password } => {
                if password.is_empty() {
                    return Err(VaultError::InvalidProfile("password cannot be empty"));
                }
                Ok(Self::Password {
                    password: StoredSecret::new(password),
                })
            }
            ProfileAuthenticationInput::PrivateKey {
                private_key_pem,
                passphrase,
            } => {
                if private_key_pem.trim().is_empty() {
                    return Err(VaultError::InvalidProfile("private key cannot be empty"));
                }
                Ok(Self::PrivateKey {
                    private_key_pem: StoredSecret::new(private_key_pem),
                    passphrase: passphrase.map(StoredSecret::new),
                })
            }
            ProfileAuthenticationInput::PrivateKeyFile { private_key_path } => {
                validate_private_key_path(&private_key_path)?;
                Ok(Self::PrivateKeyFile { private_key_path })
            }
        }
    }
}

/// IPC input only.  Do not derive `Debug`: password and key values must not reach logs.
#[derive(Clone, PartialEq, Eq, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum ProfileAuthenticationInput {
    Agent,
    Password {
        password: String,
    },
    PrivateKey {
        private_key_pem: String,
        passphrase: Option<String>,
    },
    PrivateKeyFile {
        private_key_path: String,
    },
}

impl fmt::Debug for ProfileAuthenticationInput {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Agent => formatter.write_str("ProfileAuthenticationInput::Agent"),
            Self::Password { .. } => {
                formatter.write_str("ProfileAuthenticationInput::Password([REDACTED])")
            }
            Self::PrivateKey { .. } => {
                formatter.write_str("ProfileAuthenticationInput::PrivateKey([REDACTED])")
            }
            Self::PrivateKeyFile { .. } => {
                formatter.write_str("ProfileAuthenticationInput::PrivateKeyFile([REDACTED])")
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

#[derive(Clone, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SshProfilePatch {
    pub label: Option<String>,
    pub host: Option<String>,
    pub port: Option<u16>,
    pub username: Option<String>,
    pub authentication: Option<ProfileAuthenticationInput>,
    pub tags: Option<Vec<String>>,
    pub terminal: Option<TerminalPreferences>,
}

impl fmt::Debug for SshProfilePatch {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("SshProfilePatch")
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

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
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

impl fmt::Debug for SshProfile {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter
            .debug_struct("SshProfile")
            .field("id", &self.id)
            .field("label", &self.label)
            .field("host", &self.host)
            .field("port", &self.port)
            .field("username", &self.username)
            .field("authentication", &self.authentication)
            .field("tags", &self.tags)
            .field("terminal", &self.terminal)
            .field("created_at_unix_ms", &self.created_at_unix_ms)
            .field("updated_at_unix_ms", &self.updated_at_unix_ms)
            .finish()
    }
}

impl SshProfile {
    pub fn from_input(input: SshProfileInput, now_unix_ms: u64) -> Result<Self> {
        let profile = Self {
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
        };
        profile.validate()?;
        Ok(profile)
    }

    pub fn apply_patch(&mut self, patch: SshProfilePatch, now_unix_ms: u64) -> Result<()> {
        if let Some(label) = patch.label {
            self.label = label;
        }
        if let Some(host) = patch.host {
            self.host = host;
        }
        if let Some(port) = patch.port {
            self.port = port;
        }
        if let Some(username) = patch.username {
            self.username = username;
        }
        if let Some(authentication) = patch.authentication {
            self.authentication = StoredAuthentication::from_input(authentication)?;
        }
        if let Some(tags) = patch.tags {
            self.tags = tags;
        }
        if let Some(terminal) = patch.terminal {
            self.terminal = terminal;
        }
        self.validate()?;
        self.updated_at_unix_ms = now_unix_ms;
        Ok(())
    }

    pub fn summary(&self) -> ProfileSummary {
        ProfileSummary {
            id: self.id,
            label: self.label.clone(),
            host: self.host.clone(),
            port: self.port,
            username: self.username.clone(),
            authentication_kind: self.authentication.kind(),
            tags: self.tags.clone(),
            terminal: self.terminal.clone(),
            created_at_unix_ms: self.created_at_unix_ms,
            updated_at_unix_ms: self.updated_at_unix_ms,
        }
    }

    pub fn validate(&self) -> Result<()> {
        validate_label(&self.label)?;
        validate_host(&self.host)?;
        if self.port == 0 {
            return Err(VaultError::InvalidProfile(
                "port must be between 1 and 65535",
            ));
        }
        validate_username(&self.username)?;
        validate_tags(&self.tags)?;
        validate_terminal(&self.terminal)?;
        match &self.authentication {
            StoredAuthentication::Password { password } if password.is_empty() => {
                return Err(VaultError::InvalidProfile("password cannot be empty"));
            }
            StoredAuthentication::PrivateKey {
                private_key_pem, ..
            } if private_key_pem.is_empty() => {
                return Err(VaultError::InvalidProfile("private key cannot be empty"));
            }
            StoredAuthentication::PrivateKeyFile { private_key_path } => {
                validate_private_key_path(private_key_path)?;
            }
            _ => {}
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProfileSummary {
    pub id: Uuid,
    pub label: String,
    pub host: String,
    pub port: u16,
    pub username: String,
    pub authentication_kind: AuthenticationKind,
    pub tags: Vec<String>,
    pub terminal: TerminalPreferences,
    pub created_at_unix_ms: u64,
    pub updated_at_unix_ms: u64,
}

fn validate_label(label: &str) -> Result<()> {
    let trimmed = label.trim();
    if trimmed.is_empty() || trimmed.len() > 120 || contains_control(trimmed) {
        return Err(VaultError::InvalidProfile(
            "label must be 1-120 printable characters",
        ));
    }
    Ok(())
}

fn validate_host(host: &str) -> Result<()> {
    let trimmed = host.trim();
    if trimmed.is_empty()
        || trimmed.len() > 255
        || trimmed.chars().any(|c| c.is_whitespace() || c.is_control())
        // Windows Terminal uses semicolons as command separators.  NekoSSH hands the host to
        // that terminal as a direct argv item, but rejecting the separator keeps the handoff
        // unambiguous across terminal versions.
        || trimmed.contains(';')
    {
        return Err(VaultError::InvalidProfile(
            "host must not contain whitespace or control characters",
        ));
    }
    Ok(())
}

fn validate_username(username: &str) -> Result<()> {
    if username.trim().is_empty()
        || username.len() > 128
        || username
            .chars()
            .any(|c| c.is_whitespace() || c.is_control())
        || username.contains(';')
    {
        return Err(VaultError::InvalidProfile(
            "username must not contain whitespace or control characters",
        ));
    }
    Ok(())
}

fn validate_tags(tags: &[String]) -> Result<()> {
    if tags.len() > 32 {
        return Err(VaultError::InvalidProfile("at most 32 tags are allowed"));
    }
    for tag in tags {
        if tag.trim().is_empty() || tag.len() > 48 || contains_control(tag) {
            return Err(VaultError::InvalidProfile(
                "each tag must be 1-48 printable characters",
            ));
        }
    }
    Ok(())
}

fn validate_terminal(terminal: &TerminalPreferences) -> Result<()> {
    if terminal.columns == 0 || terminal.rows == 0 || terminal.columns > 500 || terminal.rows > 500
    {
        return Err(VaultError::InvalidProfile(
            "terminal dimensions must be between 1 and 500",
        ));
    }
    if terminal.command.as_ref().is_some_and(|command| {
        command.trim().is_empty() || command.len() > 1024 || contains_control(command)
    }) {
        return Err(VaultError::InvalidProfile("terminal command is invalid"));
    }
    Ok(())
}

/// External OpenSSH receives this value as the argument after `-i`, never through a shell.
/// Requiring an absolute, non-control-character path avoids surprising cwd expansion and makes
/// an imported profile's key-file behavior explicit.
fn validate_private_key_path(private_key_path: &str) -> Result<()> {
    let path = Path::new(private_key_path);
    if private_key_path.trim().is_empty()
        || private_key_path.len() > 4_096
        || contains_control(private_key_path)
        || private_key_path.contains(';')
        || !path.is_absolute()
    {
        return Err(VaultError::InvalidProfile(
            "private key file must be an absolute, printable path",
        ));
    }
    Ok(())
}

fn contains_control(value: &str) -> bool {
    value.chars().any(char::is_control)
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum GitProvider {
    Github,
    Gitea,
    Generic,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SyncKind {
    WebDav,
    Git,
}

/// IPC input only.  It intentionally has a redacting `Debug` implementation.
#[derive(Clone, PartialEq, Eq, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum SyncConfigurationInput {
    WebDav {
        endpoint: String,
        remote_path: String,
        username: Option<String>,
        secret: String,
    },
    Git {
        provider: GitProvider,
        remote_url: String,
        branch: String,
        access_token: Option<String>,
    },
}

impl fmt::Debug for SyncConfigurationInput {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::WebDav {
                endpoint,
                remote_path,
                username,
                ..
            } => formatter
                .debug_struct("SyncConfigurationInput::WebDav")
                .field("endpoint", endpoint)
                .field("remote_path", remote_path)
                .field("username", username)
                .field("secret", &"[REDACTED]")
                .finish(),
            Self::Git {
                provider,
                remote_url,
                branch,
                ..
            } => formatter
                .debug_struct("SyncConfigurationInput::Git")
                .field("provider", provider)
                .field("remote_url", remote_url)
                .field("branch", branch)
                .field("access_token", &"[REDACTED]")
                .finish(),
        }
    }
}

#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum SyncConfiguration {
    WebDav {
        endpoint: String,
        remote_path: String,
        username: Option<String>,
        secret: StoredSecret,
    },
    Git {
        provider: GitProvider,
        remote_url: String,
        branch: String,
        access_token: Option<StoredSecret>,
    },
}

impl fmt::Debug for SyncConfiguration {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::WebDav {
                endpoint,
                remote_path,
                username,
                ..
            } => formatter
                .debug_struct("SyncConfiguration::WebDav")
                .field("endpoint", endpoint)
                .field("remote_path", remote_path)
                .field("username", username)
                .field("secret", &"[REDACTED]")
                .finish(),
            Self::Git {
                provider,
                remote_url,
                branch,
                ..
            } => formatter
                .debug_struct("SyncConfiguration::Git")
                .field("provider", provider)
                .field("remote_url", remote_url)
                .field("branch", branch)
                .field("access_token", &"[REDACTED]")
                .finish(),
        }
    }
}

impl SyncConfiguration {
    pub fn from_input(input: SyncConfigurationInput) -> Result<Self> {
        match input {
            SyncConfigurationInput::WebDav {
                endpoint,
                remote_path,
                username,
                secret,
            } => {
                let endpoint = normalize_webdav_endpoint(&endpoint)?;
                validate_remote_path(&remote_path)?;
                if username.as_ref().is_some_and(|name| {
                    name.trim().is_empty()
                        || name.len() > 256
                        || name.contains(':')
                        || name
                            .chars()
                            .any(|character| character.is_whitespace() || character.is_control())
                }) {
                    return Err(VaultError::InvalidSync("WebDAV username is invalid"));
                }
                if secret.is_empty() || contains_control(&secret) {
                    return Err(VaultError::InvalidSync("WebDAV credential is invalid"));
                }
                Ok(Self::WebDav {
                    endpoint,
                    remote_path,
                    username,
                    secret: StoredSecret::new(secret),
                })
            }
            SyncConfigurationInput::Git {
                provider,
                remote_url,
                branch,
                access_token,
            } => {
                validate_git_remote(&remote_url)?;
                validate_branch(&branch)?;
                if access_token
                    .as_ref()
                    .is_some_and(|token| token.is_empty() || contains_control(token))
                {
                    return Err(VaultError::InvalidSync("Git access token is invalid"));
                }
                Ok(Self::Git {
                    provider,
                    remote_url,
                    branch,
                    access_token: access_token.map(StoredSecret::new),
                })
            }
        }
    }

    pub fn summary(&self) -> SyncConfigurationSummary {
        match self {
            Self::WebDav {
                endpoint,
                remote_path,
                secret,
                ..
            } => SyncConfigurationSummary {
                kind: SyncKind::WebDav,
                endpoint_or_remote: endpoint.clone(),
                remote_path: Some(remote_path.clone()),
                branch: None,
                provider: None,
                credential_configured: !secret.is_empty(),
            },
            Self::Git {
                provider,
                remote_url,
                branch,
                access_token,
            } => SyncConfigurationSummary {
                kind: SyncKind::Git,
                endpoint_or_remote: remote_url.clone(),
                remote_path: None,
                branch: Some(branch.clone()),
                provider: Some(*provider),
                credential_configured: access_token.is_some(),
            },
        }
    }

    pub fn kind(&self) -> SyncKind {
        match self {
            Self::WebDav { .. } => SyncKind::WebDav,
            Self::Git { .. } => SyncKind::Git,
        }
    }

    pub fn validate(&self) -> Result<()> {
        match self {
            Self::WebDav {
                endpoint,
                remote_path,
                username,
                secret,
            } => {
                normalize_webdav_endpoint(endpoint)?;
                validate_remote_path(remote_path)?;
                if username.as_ref().is_some_and(|name| {
                    name.trim().is_empty()
                        || name.len() > 256
                        || name.contains(':')
                        || name
                            .chars()
                            .any(|character| character.is_whitespace() || character.is_control())
                }) {
                    return Err(VaultError::InvalidSync("WebDAV username is invalid"));
                }
                if secret.is_empty() || secret.with_exposed(contains_control) {
                    return Err(VaultError::InvalidSync("WebDAV credential is invalid"));
                }
            }
            Self::Git {
                remote_url,
                branch,
                access_token,
                ..
            } => {
                validate_git_remote(remote_url)?;
                validate_branch(branch)?;
                if access_token
                    .as_ref()
                    .is_some_and(|token| token.is_empty() || token.with_exposed(contains_control))
                {
                    return Err(VaultError::InvalidSync("Git access token is invalid"));
                }
            }
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncConfigurationSummary {
    pub kind: SyncKind,
    pub endpoint_or_remote: String,
    pub remote_path: Option<String>,
    pub branch: Option<String>,
    pub provider: Option<GitProvider>,
    pub credential_configured: bool,
}

/// Validates a WebDAV collection URL and makes its trailing slash explicit before a remote path
/// is appended.  Without that slash, `Url::join` would treat the last collection segment as a
/// file and silently upload a backup to the wrong location.
pub(crate) fn normalize_webdav_endpoint(endpoint: &str) -> Result<String> {
    if endpoint.trim() != endpoint {
        return Err(VaultError::InvalidSync(
            "WebDAV endpoint must not have surrounding whitespace",
        ));
    }
    let mut parsed = Url::parse(endpoint)
        .map_err(|_| VaultError::InvalidSync("WebDAV endpoint is not a URL"))?;
    if parsed.scheme() != "https" || parsed.host_str().is_none() {
        return Err(VaultError::InvalidSync("WebDAV must use an HTTPS endpoint"));
    }
    if !parsed.username().is_empty()
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
    {
        return Err(VaultError::InvalidSync(
            "WebDAV endpoint must not embed credentials, a query, or a fragment",
        ));
    }
    if !parsed.path().ends_with('/') {
        let normalized_path = format!("{}/", parsed.path());
        parsed.set_path(&normalized_path);
    }
    Ok(parsed.into())
}

fn validate_remote_path(remote_path: &str) -> Result<()> {
    let path = remote_path.trim();
    if path.is_empty()
        || path.len() > 512
        || path.starts_with('/')
        || path.contains('\\')
        || path.contains('?')
        || path.contains('#')
        || path.contains('%')
        || path.chars().any(char::is_whitespace)
        || path
            .split('/')
            .any(|part| part.is_empty() || part == "." || part == "..")
        || contains_control(path)
    {
        return Err(VaultError::InvalidSync(
            "remote path must be a clean relative path",
        ));
    }
    Ok(())
}

fn validate_git_remote(remote: &str) -> Result<()> {
    let remote = remote.trim();
    if remote.is_empty()
        || remote.len() > 2048
        || remote.chars().any(|c| c.is_whitespace() || c.is_control())
    {
        return Err(VaultError::InvalidSync("Git remote is invalid"));
    }
    if remote.starts_with("https://") || remote.starts_with("ssh://") {
        let parsed =
            Url::parse(remote).map_err(|_| VaultError::InvalidSync("Git remote is not a URL"))?;
        if parsed.host_str().is_none()
            || parsed.password().is_some()
            || parsed.query().is_some()
            || parsed.fragment().is_some()
        {
            return Err(VaultError::InvalidSync(
                "Git remote must not embed credentials, a query, or a fragment",
            ));
        }
        if parsed.scheme() == "https" && !parsed.username().is_empty() {
            return Err(VaultError::InvalidSync(
                "HTTPS Git remote must not embed credentials",
            ));
        }
        return Ok(());
    }
    // Standard OpenSSH scp-style Git remote, e.g. git@github.com:owner/repository.git.
    let Some((user_host, path)) = remote.split_once(':') else {
        return Err(VaultError::InvalidSync(
            "Git remote must use HTTPS, SSH, or scp-style syntax",
        ));
    };
    if !user_host.starts_with("git@")
        || user_host.len() <= 4
        || path.is_empty()
        || path.starts_with('/')
    {
        return Err(VaultError::InvalidSync("scp-style Git remote is invalid"));
    }
    Ok(())
}

fn validate_branch(branch: &str) -> Result<()> {
    if branch.is_empty()
        || branch.len() > 255
        || branch.starts_with('/')
        || branch.ends_with('/')
        || branch.contains("..")
        || branch.contains("//")
        || branch.ends_with('.')
        || branch.chars().any(|c| {
            c.is_whitespace()
                || c.is_control()
                || c == '~'
                || c == '^'
                || c == ':'
                || c == '?'
                || c == '*'
                || c == '['
                || c == '\\'
        })
    {
        return Err(VaultError::InvalidSync("Git branch is invalid"));
    }
    Ok(())
}

/// Normalizes a URL that may be handed to the operating system's default browser.
///
/// Arbitrary schemes (`file:`, `javascript:`, custom protocol handlers, etc.) are refused.
/// Plain HTTP is permitted only for loopback addresses so local development/web sessions remain
/// usable without silently weakening normal external browsing.
pub fn validate_external_browser_url(value: &str) -> Result<String> {
    if value.len() > 2_048 {
        return Err(VaultError::InvalidBrowserUrl("URL is too long"));
    }
    let url = Url::parse(value).map_err(|_| VaultError::InvalidBrowserUrl("URL is malformed"))?;
    let host = url
        .host_str()
        .ok_or(VaultError::InvalidBrowserUrl("URL must include a host"))?;
    if !url.username().is_empty() || url.password().is_some() {
        return Err(VaultError::InvalidBrowserUrl(
            "URL must not embed credentials",
        ));
    }
    match url.scheme() {
        "https" => Ok(url.to_string()),
        "http" if is_loopback_host(host) => Ok(url.to_string()),
        "http" => Err(VaultError::InvalidBrowserUrl(
            "external URLs must use HTTPS",
        )),
        _ => Err(VaultError::InvalidBrowserUrl(
            "only HTTPS and loopback HTTP URLs are allowed",
        )),
    }
}

fn is_loopback_host(host: &str) -> bool {
    host.eq_ignore_ascii_case("localhost")
        || host
            .parse::<IpAddr>()
            .is_ok_and(|address| address.is_loopback())
}

/// An OpenSSH-style SHA-256 fingerprint for a raw host public-key blob.
pub fn host_key_fingerprint(public_key: &[u8]) -> String {
    let digest = Sha256::digest(public_key);
    format!("SHA256:{}", STANDARD_NO_PAD.encode(digest))
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct KnownHostRecord {
    pub host: String,
    pub port: u16,
    pub key_algorithm: String,
    pub sha256_fingerprint: String,
    pub added_at_unix_ms: u64,
}

impl KnownHostRecord {
    pub fn from_presented(
        host: String,
        port: u16,
        key_algorithm: String,
        public_key: &[u8],
        now_unix_ms: u64,
    ) -> Result<Self> {
        validate_host(&host)?;
        if port == 0 || key_algorithm.trim().is_empty() || contains_control(&key_algorithm) {
            return Err(VaultError::InvalidProfile("presented host key is invalid"));
        }
        if public_key.is_empty() {
            return Err(VaultError::InvalidProfile(
                "presented host key cannot be empty",
            ));
        }
        Ok(Self {
            host,
            port,
            key_algorithm,
            sha256_fingerprint: host_key_fingerprint(public_key),
            added_at_unix_ms: now_unix_ms,
        })
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
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
        let matching_endpoint: Vec<&KnownHostRecord> = self
            .records
            .iter()
            .filter(|record| {
                record.host == host && record.port == port && record.key_algorithm == key_algorithm
            })
            .collect();
        if matching_endpoint.is_empty() {
            return HostKeyVerification::Unknown { fingerprint };
        }
        if matching_endpoint
            .iter()
            .any(|record| record.sha256_fingerprint == fingerprint)
        {
            HostKeyVerification::Trusted { fingerprint }
        } else {
            HostKeyVerification::Mismatch {
                fingerprint,
                expected_fingerprints: matching_endpoint
                    .iter()
                    .map(|record| record.sha256_fingerprint.clone())
                    .collect(),
            }
        }
    }

    /// Records an already user-approved host key.  Callers must display the fingerprint and ask
    /// for explicit approval when `verify` returns `Unknown` or `Mismatch`.
    pub fn trust(&mut self, record: KnownHostRecord) {
        if !self.records.iter().any(|existing| existing == &record) {
            self.records.push(record);
        }
    }

    pub fn records(&self) -> &[KnownHostRecord] {
        &self.records
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

/// A plan for a real SSH implementation.  It deliberately has no password/private-key field.
/// A production session implementation must use a maintained SSH transport (or system OpenSSH)
/// and apply the `KnownHosts` decision before opening the channel.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSessionPlan {
    pub profile_id: Uuid,
    pub host: String,
    pub port: u16,
    pub username: String,
    pub authentication_kind: AuthenticationKind,
    pub strict_host_key_checking: bool,
    pub execution_target: SshExecutionTarget,
    /// Present only where platform OpenSSH can authenticate without serializing vaulted key data.
    pub external_ssh: Option<ExternalSshInvocation>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SshExecutionTarget {
    /// A real `ssh` process can be safely spawned using the generated argument vector.
    SystemOpenSsh,
    /// A vaulted PEM key must be supplied to a maintained in-process SSH/PTY adapter.
    IntegratedSshAdapter,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ExternalSshInvocation {
    pub program: String,
    pub arguments: Vec<String>,
}

impl ExternalSshInvocation {
    /// Produces a real OS process command without invoking a shell.
    pub fn as_command(&self) -> Command {
        let mut command = Command::new(&self.program);
        command.args(&self.arguments);
        command
    }

    /// Launches a visible, interactive traditional terminal without putting profile data through
    /// a shell.  On Windows, `wt.exe` receives the `ssh` program and every argument directly;
    /// a GUI Tauri process has no console of its own, so spawning `ssh.exe` by itself would leave
    /// password prompts and output invisible.  On other platforms an inherited terminal is only
    /// used when NekoSSH was itself started from one; a native PTY adapter is required for a GUI
    /// terminal pane.
    pub fn spawn_interactive(&self) -> Result<Child> {
        #[cfg(windows)]
        {
            let mut command = self.windows_terminal_command()?;
            command.spawn().map_err(|error| {
                if error.kind() == std::io::ErrorKind::NotFound {
                    VaultError::ExternalTerminalUnavailable
                } else {
                    VaultError::ExternalSshLaunchFailed
                }
            })
        }

        #[cfg(not(windows))]
        {
            use std::io::IsTerminal;

            if !std::io::stdin().is_terminal()
                || !std::io::stdout().is_terminal()
                || !std::io::stderr().is_terminal()
            {
                return Err(VaultError::ExternalTerminalUnavailable);
            }

            let mut command = self.as_command();
            command
                .stdin(Stdio::inherit())
                .stdout(Stdio::inherit())
                .stderr(Stdio::inherit());
            command
                .spawn()
                .map_err(|_| VaultError::ExternalSshLaunchFailed)
        }
    }

    /// Builds the Windows Terminal launcher command using argv only.  This deliberately avoids
    /// `cmd.exe`, PowerShell, and `Start-Process`, so host names, usernames, and key paths never
    /// cross a command-string parser.
    #[cfg(windows)]
    fn windows_terminal_command(&self) -> Result<Command> {
        let terminal_program = trusted_windows_terminal_program()?;
        let trusted_ssh_program = trusted_system_openssh_program()?;
        if Path::new(&self.program) != trusted_ssh_program {
            return Err(VaultError::ExternalSshUnavailable);
        }
        Ok(self.windows_terminal_command_with_program(&terminal_program))
    }

    #[cfg(windows)]
    fn windows_terminal_command_with_program(&self, terminal_program: &Path) -> Command {
        let mut command = Command::new(terminal_program);
        command
            .args(["-w", "new", "new-tab", "--title", "NekoSSH SSH"])
            .arg(&self.program)
            .args(&self.arguments);
        command
    }

    #[cfg(test)]
    #[cfg(windows)]
    fn windows_terminal_arguments(&self) -> Vec<std::ffi::OsString> {
        self.windows_terminal_command_with_program(Path::new(
            r"C:\Users\example\AppData\Local\Microsoft\WindowsApps\wt.exe",
        ))
        .get_args()
        .map(Into::into)
        .collect()
    }

    #[cfg(test)]
    #[cfg(not(windows))]
    fn windows_terminal_arguments(&self) -> Vec<std::ffi::OsString> {
        Vec::new()
    }
}

#[cfg(windows)]
fn trusted_system_openssh_program() -> Result<PathBuf> {
    // Do not fall back to `ssh.exe` on PATH: a user-writable directory or an imported
    // environment could otherwise replace the program that receives SSH arguments.  The Windows
    // optional OpenSSH client is anchored below the OS system root instead.
    let system_root = std::env::var_os("SystemRoot")
        .ok_or(VaultError::ExternalSshUnavailable)
        .map(PathBuf::from)?;
    if !system_root.is_absolute() {
        return Err(VaultError::ExternalSshUnavailable);
    }
    let candidate = system_root.join("System32").join("OpenSSH").join("ssh.exe");
    if candidate.is_file() {
        Ok(candidate)
    } else {
        Err(VaultError::ExternalSshUnavailable)
    }
}

#[cfg(windows)]
fn trusted_windows_terminal_program() -> Result<PathBuf> {
    // `wt.exe` is the Windows-managed App Execution Alias.  Its explicit, well-known path avoids
    // PATH resolution while still supporting the Store/winget Windows Terminal installation.
    let local_app_data = std::env::var_os("LOCALAPPDATA")
        .ok_or(VaultError::ExternalTerminalUnavailable)
        .map(PathBuf::from)?;
    if !local_app_data.is_absolute() {
        return Err(VaultError::ExternalTerminalUnavailable);
    }
    let candidate = local_app_data
        .join("Microsoft")
        .join("WindowsApps")
        .join("wt.exe");
    if candidate.is_file() {
        Ok(candidate)
    } else {
        Err(VaultError::ExternalTerminalUnavailable)
    }
}

fn system_openssh_program() -> Result<String> {
    #[cfg(windows)]
    {
        trusted_system_openssh_program().map(|path| path.to_string_lossy().into_owned())
    }

    #[cfg(not(windows))]
    {
        Ok("ssh".to_owned())
    }
}

fn validate_managed_known_hosts_path(path: &Path) -> Result<()> {
    let rendered = path.to_string_lossy();
    if !path.is_absolute()
        || rendered.is_empty()
        || rendered.len() > 4_096
        || contains_control(&rendered)
        // Windows Terminal treats semicolons as command separators.  Rejecting one in the
        // app-managed path keeps the hand-off unambiguous even if a user profile directory has
        // an unusual name.
        || rendered.contains(';')
    {
        return Err(VaultError::ExternalSshUnavailable);
    }
    Ok(())
}

impl SshProfile {
    /// Builds a traditional OpenSSH invocation without ever placing vaulted password/key data on
    /// the command line.  The actual session UI may prompt or hand credential material to a real
    /// protocol library over an in-memory channel.
    pub fn session_plan(&self, known_hosts_file: &Path) -> Result<SshSessionPlan> {
        validate_managed_known_hosts_path(known_hosts_file)?;
        let mut arguments = vec![
            "-p".to_owned(),
            self.port.to_string(),
            "-l".to_owned(),
            self.username.clone(),
            "-o".to_owned(),
            // `accept-new` records a first-seen key in the app-owned file but refuses any
            // changed key on later connections.  This keeps first use practical without falling
            // back to `StrictHostKeyChecking=no`.
            "StrictHostKeyChecking=accept-new".to_owned(),
        ];
        arguments.push("-o".to_owned());
        arguments.push(format!("UserKnownHostsFile={}", known_hosts_file.display()));
        // Do not silently trust host keys from a machine-wide OpenSSH file.  NekoSSH owns its
        // trust-on-first-use store and OpenSSH is the sole writer for it.
        arguments.push("-o".to_owned());
        arguments.push("GlobalKnownHostsFile=none".to_owned());
        arguments.push("-o".to_owned());
        arguments.push("UpdateHostKeys=no".to_owned());

        let (execution_target, external_ssh) = match &self.authentication {
            StoredAuthentication::PrivateKey { .. } => {
                // Materializing a private key to a temp file would undermine the encrypted vault.
                // A maintained SSH library/PTY adapter must consume it from protected memory.
                (SshExecutionTarget::IntegratedSshAdapter, None)
            }
            StoredAuthentication::PrivateKeyFile { private_key_path } => {
                arguments.push("-i".to_owned());
                arguments.push(private_key_path.clone());
                arguments.push("-o".to_owned());
                arguments.push("IdentitiesOnly=yes".to_owned());
                arguments.push("--".to_owned());
                arguments.push(self.host.clone());
                (
                    SshExecutionTarget::SystemOpenSsh,
                    Some(ExternalSshInvocation {
                        program: system_openssh_program()?,
                        arguments,
                    }),
                )
            }
            StoredAuthentication::Agent | StoredAuthentication::Password { .. } => {
                arguments.push("--".to_owned());
                arguments.push(self.host.clone());
                (
                    SshExecutionTarget::SystemOpenSsh,
                    Some(ExternalSshInvocation {
                        program: system_openssh_program()?,
                        arguments,
                    }),
                )
            }
        };

        Ok(SshSessionPlan {
            profile_id: self.id,
            host: self.host.clone(),
            port: self.port,
            username: self.username.clone(),
            authentication_kind: self.authentication.kind(),
            strict_host_key_checking: true,
            execution_target,
            external_ssh,
        })
    }

    pub fn launch_external_ssh(&self, known_hosts_file: &Path) -> Result<u32> {
        let plan = self.session_plan(known_hosts_file)?;
        let invocation = plan
            .external_ssh
            .ok_or(VaultError::ExternalSshUnavailable)?;
        let child = invocation.spawn_interactive()?;
        Ok(child.id())
    }
}

/// The integration boundary for a real, audited SSH transport.  NekoSSH intentionally provides
/// no pretend or home-grown SSH cipher implementation.
pub trait VerifiedSshSessionBackend {
    type Session;
    type Error: std::error::Error + Send + Sync + 'static;

    fn open_verified(
        &self,
        plan: &SshSessionPlan,
    ) -> std::result::Result<Self::Session, Self::Error>;
}

#[cfg(test)]
mod tests {
    use super::*;

    fn test_known_hosts_path() -> PathBuf {
        if cfg!(windows) {
            PathBuf::from(r"C:\NekoSSH\neko233-known_hosts")
        } else {
            PathBuf::from("/tmp/nekossh-known_hosts")
        }
    }

    #[test]
    fn known_hosts_requires_explicit_trust_and_detects_mismatch() {
        let mut known_hosts = KnownHosts::default();
        let presented = b"host-public-key-1";
        assert!(matches!(
            known_hosts.verify("db.internal", 22, "ssh-ed25519", presented),
            HostKeyVerification::Unknown { .. }
        ));

        known_hosts.trust(
            KnownHostRecord::from_presented(
                "db.internal".to_owned(),
                22,
                "ssh-ed25519".to_owned(),
                presented,
                1,
            )
            .expect("valid host record"),
        );
        assert!(matches!(
            known_hosts.verify("db.internal", 22, "ssh-ed25519", presented),
            HostKeyVerification::Trusted { .. }
        ));
        assert!(matches!(
            known_hosts.verify("db.internal", 22, "ssh-ed25519", b"changed-key"),
            HostKeyVerification::Mismatch { .. }
        ));
    }

    #[test]
    fn external_ssh_plan_cannot_contain_vaulted_password() {
        let profile = SshProfile::from_input(
            SshProfileInput {
                label: "Production".to_owned(),
                host: "prod.example.test".to_owned(),
                port: 22,
                username: "deploy".to_owned(),
                authentication: ProfileAuthenticationInput::Password {
                    password: "this-password-is-never-an-argument".to_owned(),
                },
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            },
            1,
        )
        .expect("valid profile");
        let known_hosts = test_known_hosts_path();
        let plan = profile
            .session_plan(&known_hosts)
            .expect("external SSH plan");
        let serialized = serde_json::to_string(&plan).expect("serialize plan");
        assert!(serialized.contains("StrictHostKeyChecking=accept-new"));
        assert!(serialized.contains("UserKnownHostsFile"));
        assert!(serialized.contains("GlobalKnownHostsFile=none"));
        assert!(!serialized.contains("this-password-is-never-an-argument"));
        assert_eq!(plan.execution_target, SshExecutionTarget::SystemOpenSsh);
        assert!(plan.external_ssh.is_some());
    }

    #[test]
    fn key_file_open_ssh_arguments_are_strict_and_shell_free() {
        let private_key_path = if cfg!(windows) {
            r"C:\Users\example\.ssh\id_ed25519"
        } else {
            "/Users/example/.ssh/id_ed25519"
        };
        let profile = SshProfile::from_input(
            SshProfileInput {
                label: "Key-file server".to_owned(),
                host: "git.example.test".to_owned(),
                port: 2222,
                username: "git".to_owned(),
                authentication: ProfileAuthenticationInput::PrivateKeyFile {
                    private_key_path: private_key_path.to_owned(),
                },
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            },
            1,
        )
        .expect("valid profile");
        let known_hosts = test_known_hosts_path();
        let plan = profile
            .session_plan(&known_hosts)
            .expect("external SSH plan");
        let invocation = plan.external_ssh.expect("external OpenSSH plan");
        let command = invocation.as_command();
        let arguments: Vec<_> = command
            .get_args()
            .map(|argument| argument.to_string_lossy().into_owned())
            .collect();
        let program = command.get_program().to_string_lossy();
        if cfg!(windows) {
            assert!(program.to_ascii_lowercase().ends_with("ssh.exe"));
        } else {
            assert_eq!(program, "ssh");
        }
        assert!(
            arguments
                .windows(2)
                .any(|pair| pair[0] == "-i" && pair[1] == private_key_path)
        );
        assert!(
            arguments
                .windows(2)
                .any(|pair| pair[0] == "--" && pair[1] == "git.example.test")
        );
        assert!(
            arguments
                .iter()
                .any(|argument| argument == "StrictHostKeyChecking=accept-new")
        );
        assert!(arguments.iter().any(|argument| {
            argument == &format!("UserKnownHostsFile={}", known_hosts.display())
        }));
        assert!(
            arguments
                .iter()
                .any(|argument| argument == "GlobalKnownHostsFile=none")
        );
        assert!(
            arguments
                .iter()
                .any(|argument| argument == "IdentitiesOnly=yes")
        );
    }

    #[test]
    fn vaulted_pem_key_requires_a_real_integrated_transport() {
        let profile = SshProfile::from_input(
            SshProfileInput {
                label: "Vaulted key".to_owned(),
                host: "git.example.test".to_owned(),
                port: 22,
                username: "git".to_owned(),
                authentication: ProfileAuthenticationInput::PrivateKey {
                    private_key_pem: "-----BEGIN PRIVATE KEY----- private".to_owned(),
                    passphrase: None,
                },
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            },
            1,
        )
        .expect("valid profile");
        let known_hosts = test_known_hosts_path();
        let plan = profile
            .session_plan(&known_hosts)
            .expect("integrated SSH plan");
        assert_eq!(
            plan.execution_target,
            SshExecutionTarget::IntegratedSshAdapter
        );
        assert!(plan.external_ssh.is_none());
        assert!(matches!(
            profile.launch_external_ssh(&known_hosts),
            Err(VaultError::ExternalSshUnavailable)
        ));
    }

    #[test]
    fn sync_configuration_rejects_credentials_in_urls() {
        assert!(matches!(
            SyncConfiguration::from_input(SyncConfigurationInput::WebDav {
                endpoint: "https://alice:secret@example.test/dav".to_owned(),
                remote_path: "nekossh/vault.bundle".to_owned(),
                username: Some("alice".to_owned()),
                secret: "token".to_owned(),
            }),
            Err(VaultError::InvalidSync(_))
        ));
    }

    #[test]
    fn webdav_configuration_normalizes_collection_and_rejects_url_ambiguity() {
        let configuration = SyncConfiguration::from_input(SyncConfigurationInput::WebDav {
            endpoint: "https://dav.example.test/collection".to_owned(),
            remote_path: "nekossh/vault.bundle".to_owned(),
            username: Some("alice".to_owned()),
            secret: "a-safe-webdav-secret".to_owned(),
        })
        .expect("valid WebDAV configuration");
        let SyncConfiguration::WebDav { endpoint, .. } = configuration else {
            panic!("expected WebDAV configuration");
        };
        assert_eq!(endpoint, "https://dav.example.test/collection/");

        for endpoint in [
            "https://dav.example.test/collection?access_token=secret",
            "https://dav.example.test/collection#fragment",
        ] {
            assert!(matches!(
                SyncConfiguration::from_input(SyncConfigurationInput::WebDav {
                    endpoint: endpoint.to_owned(),
                    remote_path: "nekossh/vault.bundle".to_owned(),
                    username: Some("alice".to_owned()),
                    secret: "a-safe-webdav-secret".to_owned(),
                }),
                Err(VaultError::InvalidSync(_))
            ));
        }
        assert!(matches!(
            SyncConfiguration::from_input(SyncConfigurationInput::WebDav {
                endpoint: "https://dav.example.test/collection/".to_owned(),
                remote_path: "nekossh/%2e%2e/vault.bundle".to_owned(),
                username: Some("alice".to_owned()),
                secret: "a-safe-webdav-secret".to_owned(),
            }),
            Err(VaultError::InvalidSync(_))
        ));
    }

    #[test]
    fn git_remote_rejects_query_or_fragment_secrets() {
        for remote_url in [
            "https://github.com/example/nekossh.git?access_token=secret",
            "ssh://git@gitea.example.test/owner/nekossh.git#token",
        ] {
            assert!(matches!(
                SyncConfiguration::from_input(SyncConfigurationInput::Git {
                    provider: GitProvider::Generic,
                    remote_url: remote_url.to_owned(),
                    branch: "main".to_owned(),
                    access_token: None,
                }),
                Err(VaultError::InvalidSync(_))
            ));
        }
    }

    #[test]
    fn ssh_arguments_reject_windows_terminal_command_separator() {
        assert!(matches!(
            SshProfile::from_input(
                SshProfileInput {
                    label: "invalid host".to_owned(),
                    host: "example.test;new-tab cmd.exe".to_owned(),
                    port: 22,
                    username: "deploy".to_owned(),
                    authentication: ProfileAuthenticationInput::Agent,
                    tags: Vec::new(),
                    terminal: TerminalPreferences::default(),
                },
                1,
            ),
            Err(VaultError::InvalidProfile(_))
        ));
    }

    #[test]
    fn app_known_hosts_path_cannot_contain_a_terminal_command_separator() {
        let profile = SshProfile::from_input(
            SshProfileInput {
                label: "Agent server".to_owned(),
                host: "agent.example.test".to_owned(),
                port: 22,
                username: "deploy".to_owned(),
                authentication: ProfileAuthenticationInput::Agent,
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            },
            1,
        )
        .expect("valid profile");
        let unsafe_path = if cfg!(windows) {
            PathBuf::from(r"C:\NekoSSH;new-tab\known_hosts")
        } else {
            PathBuf::from("/tmp/nekossh;unsafe-known-hosts")
        };
        assert!(matches!(
            profile.session_plan(&unsafe_path),
            Err(VaultError::ExternalSshUnavailable)
        ));
    }

    #[cfg(windows)]
    #[test]
    fn windows_external_ssh_uses_windows_terminal_without_a_shell() {
        let profile = SshProfile::from_input(
            SshProfileInput {
                label: "Windows server".to_owned(),
                host: "win.example.test".to_owned(),
                port: 2222,
                username: "deploy".to_owned(),
                authentication: ProfileAuthenticationInput::Agent,
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            },
            1,
        )
        .expect("valid profile");
        let invocation = profile
            .session_plan(&test_known_hosts_path())
            .expect("external SSH plan")
            .external_ssh
            .expect("external OpenSSH plan");
        let arguments: Vec<_> = invocation
            .windows_terminal_arguments()
            .into_iter()
            .map(|argument| argument.to_string_lossy().into_owned())
            .collect();
        assert_eq!(
            &arguments[..5],
            ["-w", "new", "new-tab", "--title", "NekoSSH SSH"]
        );
        assert_eq!(arguments[5], invocation.program);
        assert!(arguments.iter().any(|argument| argument == "--"));
        assert!(
            arguments
                .iter()
                .any(|argument| argument == "win.example.test")
        );
        assert!(!arguments.iter().any(|argument| argument == "cmd.exe"));
        assert!(
            !arguments
                .iter()
                .any(|argument| argument == "powershell.exe")
        );
    }

    #[test]
    fn browser_urls_are_scheme_restricted() {
        assert!(validate_external_browser_url("https://macos.kimi.site/").is_ok());
        assert!(validate_external_browser_url("http://127.0.0.1:1420/").is_ok());
        assert!(matches!(
            validate_external_browser_url("http://example.test/"),
            Err(VaultError::InvalidBrowserUrl(_))
        ));
        assert!(matches!(
            validate_external_browser_url("file:///sensitive-file"),
            Err(VaultError::InvalidBrowserUrl(_))
        ));
    }
}
