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
    #[error("unable to establish the SSH connection")]
    SshConnectionFailed,
    #[error("unable to complete the SSH handshake")]
    SshHandshakeFailed,
    #[error("the SSH server did not present a supported host key")]
    SshHostKeyUnavailable,
    #[error("the SSH host key is not trusted yet")]
    SshHostKeyUntrusted,
    #[error("the SSH host key does not match the trusted key")]
    SshHostKeyMismatch,
    #[error("the SSH host-key confirmation has expired")]
    SshHostKeyConfirmationExpired,
    #[error("the SSH authentication method is not supported by this desktop build")]
    SshAuthenticationUnsupported,
    #[error("SSH authentication failed")]
    SshAuthenticationFailed,
    #[error("the SSH command is invalid")]
    SshCommandInvalid,
    #[error("the SSH command could not complete")]
    SshCommandFailed,
    #[error("the remote directory path is invalid")]
    SshDirectoryPathInvalid,
    #[error("the remote directory could not be listed")]
    SshDirectoryListFailed,
    #[error("the SFTP transfer request is invalid")]
    SshTransferInputInvalid,
    #[error("too many SFTP transfer jobs are active")]
    SshTransferJobLimit,
    #[error("an SFTP transfer file exceeds the supported size")]
    SshTransferFileTooLarge,
    #[error("the combined SFTP transfer exceeds the supported size")]
    SshTransferTotalTooLarge,
    #[error("the SFTP transfer job was not found")]
    SshTransferJobNotFound,
    #[error("the SFTP transfer could not complete")]
    SshTransferFailed,
    #[error("the SSH terminal could not be started")]
    SshTerminalStartFailed,
    #[error("the SSH terminal size is invalid")]
    SshTerminalResizeInvalid,
    #[error("too many SSH terminal sessions are open")]
    SshTerminalSessionLimit,
    #[error("the SSH terminal session was not found")]
    SshTerminalSessionNotFound,
    #[error("the SSH terminal input is too large")]
    SshTerminalInputTooLarge,
    #[error("the SSH terminal input queue is busy")]
    SshTerminalInputBackpressure,
    #[error("one-click SSH key setup requires a password-authenticated profile")]
    SshKeyBootstrapRequiresPassword,
    #[error("the SSH profile changed while its key was being configured")]
    SshKeyBootstrapProfileChanged,
    #[error("SSH key setup is already running for this profile")]
    SshKeyBootstrapInProgress,
    #[error("the SSH key could not be configured on the remote host")]
    SshKeyBootstrapFailed,
    #[error("an isolated temporary SSH key file could not be prepared")]
    SshTemporaryKeyFileFailed,
    #[error("the temporary SSH key file could not be removed")]
    SshTemporaryKeyCleanupFailed,
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
            Self::SshConnectionFailed => "ssh_connection_failed",
            Self::SshHandshakeFailed => "ssh_handshake_failed",
            Self::SshHostKeyUnavailable => "ssh_host_key_unavailable",
            Self::SshHostKeyUntrusted => "ssh_host_key_untrusted",
            Self::SshHostKeyMismatch => "ssh_host_key_mismatch",
            Self::SshHostKeyConfirmationExpired => "ssh_host_key_confirmation_expired",
            Self::SshAuthenticationUnsupported => "ssh_authentication_unsupported",
            Self::SshAuthenticationFailed => "ssh_authentication_failed",
            Self::SshCommandInvalid => "ssh_command_invalid",
            Self::SshCommandFailed => "ssh_command_failed",
            Self::SshDirectoryPathInvalid => "ssh_directory_path_invalid",
            Self::SshDirectoryListFailed => "ssh_directory_list_failed",
            Self::SshTransferInputInvalid => "ssh_transfer_input_invalid",
            Self::SshTransferJobLimit => "ssh_transfer_job_limit",
            Self::SshTransferFileTooLarge => "ssh_transfer_file_too_large",
            Self::SshTransferTotalTooLarge => "ssh_transfer_total_too_large",
            Self::SshTransferJobNotFound => "ssh_transfer_job_not_found",
            Self::SshTransferFailed => "ssh_transfer_failed",
            Self::SshTerminalStartFailed => "ssh_terminal_start_failed",
            Self::SshTerminalResizeInvalid => "ssh_terminal_resize_invalid",
            Self::SshTerminalSessionLimit => "ssh_terminal_session_limit",
            Self::SshTerminalSessionNotFound => "ssh_terminal_session_not_found",
            Self::SshTerminalInputTooLarge => "ssh_terminal_input_too_large",
            Self::SshTerminalInputBackpressure => "ssh_terminal_input_backpressure",
            Self::SshKeyBootstrapRequiresPassword => "ssh_key_bootstrap_requires_password",
            Self::SshKeyBootstrapProfileChanged => "ssh_key_bootstrap_profile_changed",
            Self::SshKeyBootstrapInProgress => "ssh_key_bootstrap_in_progress",
            Self::SshKeyBootstrapFailed => "ssh_key_bootstrap_failed",
            Self::SshTemporaryKeyFileFailed => "ssh_temporary_key_file_failed",
            Self::SshTemporaryKeyCleanupFailed => "ssh_temporary_key_cleanup_failed",
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
