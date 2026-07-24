use std::io;

use thiserror::Error;

/// Errors deliberately avoid echoing passwords, tokens, profile credentials, or ciphertext.
#[derive(Debug, Error)]
pub enum VaultError {
    #[error("a vault already exists at this location")]
    AlreadyExists,
    #[error("no vault has been created yet")]
    Missing,
    #[error("the vault is locked")]
    Locked,
    #[error("the vault is already unlocked")]
    AlreadyUnlocked,
    #[error("unable to unlock vault; check the password or vault integrity")]
    UnlockFailed,
    #[error("unsupported vault format")]
    UnsupportedFormat,
    #[error("vault data is malformed")]
    Malformed,
    #[error("vault encryption operation failed")]
    EncryptionFailed,
    #[error("password must contain at least {0} characters")]
    WeakPassword(usize),
    #[error("invalid SSH profile: {0}")]
    InvalidProfile(&'static str),
    #[error("profile was not found")]
    ProfileNotFound,
    #[error("invalid sync configuration: {0}")]
    InvalidSync(&'static str),
    #[error("cloud sync has not been configured")]
    SyncNotConfigured,
    #[error("the configured sync provider is not WebDAV")]
    SyncNotWebDav,
    #[error("the configured sync provider is not Git")]
    SyncNotGit,
    #[error("the encrypted sync bundle exceeds the supported size")]
    SyncBundleTooLarge,
    #[error("the cloud backup changed remotely; download and review it before retrying")]
    SyncConflict,
    #[error("the WebDAV server did not return a safe version validator")]
    SyncVersionUnavailable,
    #[error("the cloud sync transport could not complete")]
    SyncTransportFailed,
    #[error("no trusted Git client is available for encrypted Git backup")]
    GitClientUnavailable,
    #[error("Git backup authentication requires an HTTPS vault token or an SSH-agent remote")]
    GitAuthenticationUnsupported,
    #[error("the configured Git branch does not contain an encrypted NekoSSH backup")]
    SyncRemoteBundleMissing,
    #[error(
        "Git backup was not pushed because the remote changed or rejected it; no remote data was overwritten"
    )]
    SyncGitPushRejected,
    #[error("invalid external browser URL: {0}")]
    InvalidBrowserUrl(&'static str),
    #[error("unable to open the external browser")]
    BrowserOpenFailed,
    #[error("this authentication method requires the integrated SSH terminal adapter")]
    ExternalSshUnavailable,
    #[error("no supported interactive terminal is available for external SSH")]
    ExternalTerminalUnavailable,
    #[error("unable to launch the local OpenSSH client")]
    ExternalSshLaunchFailed,
    #[error("the encrypted vault changed on disk; lock and reopen it before changing the password")]
    VaultChangedOnDisk,
    #[error("lock the vault before replacing it with an encrypted backup")]
    VaultMustBeLockedForImport,
    #[error("no verified pre-import vault backup is available")]
    VaultImportBackupMissing,
    #[error("an internal vault lock was poisoned")]
    LockPoisoned,
    #[error("vault storage error")]
    Storage(#[source] io::Error),
    #[error("serialization error")]
    Serialization(#[source] serde_json::Error),
    #[error("secure random-number generation failed")]
    Random,
}

impl VaultError {
    /// A stable IPC-facing error code.  This never exposes implementation details.
    pub const fn code(&self) -> &'static str {
        match self {
            Self::AlreadyExists => "vault_exists",
            Self::Missing => "vault_missing",
            Self::Locked => "vault_locked",
            Self::AlreadyUnlocked => "vault_already_unlocked",
            Self::UnlockFailed => "vault_unlock_failed",
            Self::UnsupportedFormat => "vault_unsupported_format",
            Self::Malformed => "vault_malformed",
            Self::EncryptionFailed => "vault_encryption_failed",
            Self::WeakPassword(_) => "weak_password",
            Self::InvalidProfile(_) => "invalid_profile",
            Self::ProfileNotFound => "profile_not_found",
            Self::InvalidSync(_) => "invalid_sync_configuration",
            Self::SyncNotConfigured => "sync_not_configured",
            Self::SyncNotWebDav => "sync_not_webdav",
            Self::SyncNotGit => "sync_not_git",
            Self::SyncBundleTooLarge => "sync_bundle_too_large",
            Self::SyncConflict => "sync_conflict",
            Self::SyncVersionUnavailable => "sync_version_unavailable",
            Self::SyncTransportFailed => "sync_transport_failed",
            Self::GitClientUnavailable => "git_client_unavailable",
            Self::GitAuthenticationUnsupported => "git_authentication_unsupported",
            Self::SyncRemoteBundleMissing => "sync_remote_bundle_missing",
            Self::SyncGitPushRejected => "sync_git_push_rejected",
            Self::InvalidBrowserUrl(_) => "invalid_browser_url",
            Self::BrowserOpenFailed => "browser_open_failed",
            Self::ExternalSshUnavailable => "external_ssh_unavailable",
            Self::ExternalTerminalUnavailable => "external_terminal_unavailable",
            Self::ExternalSshLaunchFailed => "external_ssh_launch_failed",
            Self::VaultChangedOnDisk => "vault_changed_on_disk",
            Self::VaultMustBeLockedForImport => "vault_must_be_locked_for_import",
            Self::VaultImportBackupMissing => "vault_import_backup_missing",
            Self::LockPoisoned => "vault_lock_failed",
            Self::Storage(_) => "vault_storage_failed",
            Self::Serialization(_) => "vault_serialization_failed",
            Self::Random => "secure_random_failed",
        }
    }

    /// A clear but non-sensitive explanation suitable for UI display.
    pub fn public_message(&self) -> String {
        match self {
            Self::Storage(_) => "Unable to read or write the encrypted vault.".to_owned(),
            Self::Serialization(_) | Self::Malformed => {
                "The encrypted vault is malformed or cannot be read.".to_owned()
            }
            Self::Random => "Secure random-number generation failed.".to_owned(),
            _ => self.to_string(),
        }
    }
}

pub type Result<T> = std::result::Result<T, VaultError>;
