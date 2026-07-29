//! Integrated, one-shot SSH operations.
//!
//! The webview passes only a saved profile id plus an explicit remote command or directory path.
//! Transient credentials never become command-line arguments or persistent session state. Each
//! operation creates, verifies, authenticates, uses, and drops an in-process SSH
//! connection on a blocking worker thread.

use std::{
    collections::HashMap,
    io::Read,
    net::{TcpStream, ToSocketAddrs},
    path::Path,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use serde::Serialize;
use ssh2::{ErrorCode, ExtendedData, HostKeyType, Session};
use uuid::Uuid;

use crate::{
    domain::{HostKeyVerification, SshProfile, StoredAuthentication, StoredSecret},
    error::{NekoError, Result},
    operation_admission::ProfileCloseSignal,
};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const SSH_OPERATION_TIMEOUT_MS: u32 = 20_000;
const PENDING_HOST_KEY_TTL: Duration = Duration::from_secs(5 * 60);
const MAX_PENDING_HOST_KEYS: usize = 64;
const MAX_COMMAND_BYTES: usize = 32 * 1024;
const MAX_COMMAND_OUTPUT_BYTES: usize = 1024 * 1024;
const MAX_REMOTE_PATH_BYTES: usize = 4 * 1024;
const MAX_DIRECTORY_ENTRIES: usize = 10_000;
const MAX_DIRECTORY_ENTRY_NAME_BYTES: usize = 4 * 1024;
const MAX_DIRECTORY_METADATA_BYTES: usize = 4 * 1024 * 1024;
// libssh2 uses LIBSSH2_ERROR_FILE when a raw SFTP directory handle reaches EOF.
const LIBSSH2_ERROR_FILE: i32 = -16;

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(
    tag = "status",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum SshHostKeyStatus {
    Trusted {
        host: String,
        port: u16,
        key_algorithm: String,
        fingerprint: String,
    },
    ConfirmationRequired {
        confirmation_id: Uuid,
        host: String,
        port: u16,
        key_algorithm: String,
        fingerprint: String,
    },
    Mismatch {
        host: String,
        port: u16,
        key_algorithm: String,
        fingerprint: String,
        expected_fingerprints: Vec<String>,
    },
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshCommandResult {
    /// stdout and stderr are deliberately merged in wire order, matching an interactive terminal.
    pub output: String,
    pub exit_status: i32,
    /// Echoes the requested remote working directory.  One-shot channels do not retain shell
    /// state after this response returns.
    pub working_directory: String,
    pub truncated: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshDirectoryEntry {
    pub name: String,
    pub is_directory: bool,
    pub size: Option<u64>,
    pub modified_at_unix: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshDirectoryListing {
    pub path: String,
    pub entries: Vec<SshDirectoryEntry>,
    pub truncated: bool,
}

/// Owns only ephemeral host-key confirmations.  Authenticated SSH sessions deliberately are not
/// retained: a one-shot command/file-listing API is simpler to lock, time out, and clean up than a
/// renderer-controlled terminal session.
/// Minimal source of a transient SSH profile and its host-key policy. The native IDE keeps every
/// value in memory only and never writes credentials or a known-hosts file.
pub(crate) trait SshProfileStore: Send + Sync {
    fn ssh_profile_snapshot(&self, profile_id: Uuid) -> Result<SshProfile>;

    fn verify_host_key(
        &self,
        profile_id: Uuid,
        host: &str,
        port: u16,
        key_algorithm: &str,
        public_key: &[u8],
    ) -> Result<HostKeyVerification>;

    fn trust_host_key(
        &self,
        profile_id: Uuid,
        host: String,
        port: u16,
        key_algorithm: String,
        public_key: &[u8],
    ) -> Result<()>;
}

pub struct SshService {
    profiles: Arc<dyn SshProfileStore>,
    pending_host_keys: Mutex<PendingHostKeys>,
}

impl SshService {
    pub(crate) fn new<P>(profiles: Arc<P>) -> Self
    where
        P: SshProfileStore + 'static,
    {
        Self {
            profiles,
            pending_host_keys: Mutex::new(PendingHostKeys::default()),
        }
    }

    /// Handshakes only.  Authentication happens only after the caller has approved an unknown
    /// fingerprint and a later operation re-verifies it.
    pub fn prepare(&self, profile_id: Uuid) -> Result<SshHostKeyStatus> {
        let profile = self.profiles.ssh_profile_snapshot(profile_id)?;
        let session = connect_and_handshake(&profile)?;
        let presented = presented_host_key(&session)?;

        match self.profiles.verify_host_key(
            profile_id,
            &profile.host,
            profile.port,
            &presented.key_algorithm,
            &presented.public_key,
        )? {
            HostKeyVerification::Trusted { fingerprint } => Ok(SshHostKeyStatus::Trusted {
                host: profile.host,
                port: profile.port,
                key_algorithm: presented.key_algorithm,
                fingerprint,
            }),
            HostKeyVerification::Unknown { fingerprint } => {
                let confirmation_id = self.insert_pending_host_key(PendingHostKey {
                    profile_id,
                    host: profile.host.clone(),
                    port: profile.port,
                    key_algorithm: presented.key_algorithm.clone(),
                    public_key: presented.public_key,
                    expires_at: Instant::now() + PENDING_HOST_KEY_TTL,
                })?;
                Ok(SshHostKeyStatus::ConfirmationRequired {
                    confirmation_id,
                    host: profile.host,
                    port: profile.port,
                    key_algorithm: presented.key_algorithm,
                    fingerprint,
                })
            }
            HostKeyVerification::Mismatch {
                fingerprint,
                expected_fingerprints,
            } => Ok(SshHostKeyStatus::Mismatch {
                host: profile.host,
                port: profile.port,
                key_algorithm: presented.key_algorithm,
                fingerprint,
                expected_fingerprints,
            }),
        }
    }

    /// Trusts exactly a raw key observed by `prepare`; no host/key material comes from the
    /// webview.  A fresh command/listing connection will handshake and verify this new trust
    /// record again before it authenticates.
    pub fn trust_host_key(
        &self,
        expected_profile_id: Uuid,
        confirmation_id: Uuid,
    ) -> Result<SshHostKeyStatus> {
        let pending = {
            let mut pending_host_keys = self
                .pending_host_keys
                .lock()
                .map_err(|_| NekoError::LockPoisoned)?;
            pending_host_keys.take(confirmation_id)?
        };
        if pending.profile_id != expected_profile_id {
            return Err(NekoError::SshHostKeyConfirmationExpired);
        }

        let profile = self.profiles.ssh_profile_snapshot(pending.profile_id)?;
        if profile.host != pending.host || profile.port != pending.port {
            return Err(NekoError::SshHostKeyConfirmationExpired);
        }

        self.profiles.trust_host_key(
            pending.profile_id,
            pending.host.clone(),
            pending.port,
            pending.key_algorithm.clone(),
            &pending.public_key,
        )?;

        Ok(SshHostKeyStatus::Trusted {
            host: pending.host,
            port: pending.port,
            key_algorithm: pending.key_algorithm,
            fingerprint: crate::domain::host_key_fingerprint(&pending.public_key),
        })
    }

    pub fn reject_host_key(&self, confirmation_id: Uuid) -> Result<()> {
        let mut pending_host_keys = self
            .pending_host_keys
            .lock()
            .map_err(|_| NekoError::LockPoisoned)?;
        pending_host_keys.remove(confirmation_id);
        Ok(())
    }

    /// Removes uncommitted host-key confirmations that belong to a disconnected ephemeral
    /// profile.
    pub fn forget_profile(&self, profile_id: Uuid) -> Result<()> {
        let mut pending_host_keys = self
            .pending_host_keys
            .lock()
            .map_err(|_| NekoError::LockPoisoned)?;
        pending_host_keys
            .entries
            .retain(|_, candidate| candidate.profile_id != profile_id);
        Ok(())
    }

    pub fn execute(
        &self,
        profile_id: Uuid,
        command: String,
        cwd: Option<String>,
        close_signal: ProfileCloseSignal,
    ) -> Result<SshCommandResult> {
        let (command, working_directory) = command_for_working_directory(command, cwd)?;
        let session = self.open_authenticated_for_terminal(profile_id)?;
        let mut channel = session
            .channel_session()
            .map_err(|_| NekoError::SshCommandFailed)?;
        // Merge output before execution so a large stderr stream cannot block while stdout is
        // drained.  This is the most useful shape for the current terminal-style UI.
        channel
            .handle_extended_data(ExtendedData::Merge)
            .map_err(|_| NekoError::SshCommandFailed)?;
        // Host resolution, handshake, host-key verification, authentication, and channel setup
        // may all outlive the command's initial admission. Linearize immediately before `exec`:
        // if disconnect closed the profile first, no remote command is issued.
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        channel
            .exec(&command)
            .map_err(|_| NekoError::SshCommandFailed)?;

        let (output, truncated) = read_channel_output(&mut channel, &close_signal)?;
        if truncated {
            let _ = channel.close();
        }
        let _ = channel.wait_close();
        let exit_status = channel.exit_status().unwrap_or(-1);

        Ok(SshCommandResult {
            output,
            exit_status,
            working_directory,
            truncated,
        })
    }

    pub fn list_directory(&self, profile_id: Uuid, path: String) -> Result<SshDirectoryListing> {
        validate_remote_path(&path)?;
        let session = self.open_authenticated_for_terminal(profile_id)?;
        let sftp = session
            .sftp()
            .map_err(|_| NekoError::SshDirectoryListFailed)?;
        let mut directory = sftp
            .opendir(Path::new(&path))
            .map_err(|_| NekoError::SshDirectoryListFailed)?;
        let mut entries = Vec::new();
        let mut entries_seen = 0_usize;
        let mut metadata_bytes = 0_usize;
        let mut truncated = false;

        loop {
            if entries_seen >= MAX_DIRECTORY_ENTRIES {
                truncated = true;
                break;
            }
            match directory.readdir() {
                Ok((entry_path, stat)) => {
                    entries_seen += 1;
                    let name = entry_path.file_name().unwrap_or(entry_path.as_os_str());
                    // Check the platform representation before lossy UTF-8 conversion so one
                    // malicious server-controlled name cannot force an unexpectedly large String.
                    if name.as_encoded_bytes().len() > MAX_DIRECTORY_ENTRY_NAME_BYTES {
                        truncated = true;
                        continue;
                    }
                    let name = name.to_string_lossy().into_owned();
                    if name == "." || name == ".." {
                        continue;
                    }
                    let Some(next_metadata_bytes) =
                        reserve_directory_metadata_bytes(metadata_bytes, &name)
                    else {
                        truncated = true;
                        break;
                    };
                    metadata_bytes = next_metadata_bytes;
                    entries.push(SshDirectoryEntry {
                        name,
                        is_directory: stat.is_dir(),
                        size: stat.size,
                        modified_at_unix: stat.mtime,
                    });
                }
                // ssh2's low-level File::readdir maps directory EOF to LIBSSH2_ERROR_FILE.
                Err(error) if error.code() == ErrorCode::Session(LIBSSH2_ERROR_FILE) => break,
                Err(_) => return Err(NekoError::SshDirectoryListFailed),
            }
        }

        entries.sort_by(|left, right| {
            right
                .is_directory
                .cmp(&left.is_directory)
                .then_with(|| left.name.to_lowercase().cmp(&right.name.to_lowercase()))
        });

        Ok(SshDirectoryListing {
            path,
            entries,
            truncated,
        })
    }

    fn insert_pending_host_key(&self, pending: PendingHostKey) -> Result<Uuid> {
        let mut pending_host_keys = self
            .pending_host_keys
            .lock()
            .map_err(|_| NekoError::LockPoisoned)?;
        pending_host_keys.insert(pending)
    }

    /// Opens a fresh transport only after the process-local known-hosts policy accepts the
    /// presented key.  Kept crate-private so the PTY service can use the same verification and
    /// authentication path as one-shot command/SFTP operations without exposing credentials.
    pub(crate) fn open_authenticated_for_terminal(&self, profile_id: Uuid) -> Result<Session> {
        let profile = self.profiles.ssh_profile_snapshot(profile_id)?;
        self.open_authenticated_snapshot(&profile)
    }

    /// Uses a caller-owned profile snapshot so a security-sensitive operation can pin the exact
    /// authenticated identity it is about to use. The snapshot never crosses IPC and
    /// is dropped (and its stored secrets zeroized) once the operation completes.
    pub(crate) fn open_authenticated_snapshot(&self, profile: &SshProfile) -> Result<Session> {
        let session = connect_and_handshake(profile)?;
        let presented = presented_host_key(&session)?;
        match self.profiles.verify_host_key(
            profile.id,
            &profile.host,
            profile.port,
            &presented.key_algorithm,
            &presented.public_key,
        )? {
            HostKeyVerification::Trusted { .. } => {}
            HostKeyVerification::Unknown { .. } => return Err(NekoError::SshHostKeyUntrusted),
            HostKeyVerification::Mismatch { .. } => return Err(NekoError::SshHostKeyMismatch),
        }
        authenticate(&session, profile)?;
        Ok(session)
    }
}

struct PresentedHostKey {
    key_algorithm: String,
    public_key: Vec<u8>,
}

struct PendingHostKey {
    profile_id: Uuid,
    host: String,
    port: u16,
    key_algorithm: String,
    public_key: Vec<u8>,
    expires_at: Instant,
}

#[derive(Default)]
struct PendingHostKeys {
    entries: HashMap<Uuid, PendingHostKey>,
}

impl PendingHostKeys {
    fn insert(&mut self, pending: PendingHostKey) -> Result<Uuid> {
        self.purge_expired();
        self.entries
            .retain(|_, existing| existing.profile_id != pending.profile_id);
        if self.entries.len() >= MAX_PENDING_HOST_KEYS {
            return Err(NekoError::SshHostKeyConfirmationLimit);
        }
        let confirmation_id = Uuid::new_v4();
        self.entries.insert(confirmation_id, pending);
        Ok(confirmation_id)
    }

    fn take(&mut self, confirmation_id: Uuid) -> Result<PendingHostKey> {
        self.purge_expired();
        self.entries
            .remove(&confirmation_id)
            .ok_or(NekoError::SshHostKeyConfirmationExpired)
    }

    fn remove(&mut self, confirmation_id: Uuid) {
        self.purge_expired();
        self.entries.remove(&confirmation_id);
    }

    fn purge_expired(&mut self) {
        let now = Instant::now();
        self.entries.retain(|_, pending| pending.expires_at > now);
    }
}

fn connect_and_handshake(profile: &SshProfile) -> Result<Session> {
    let addresses = (profile.host.as_str(), profile.port)
        .to_socket_addrs()
        .map_err(|_| NekoError::SshConnectionFailed)?;
    for address in addresses {
        match TcpStream::connect_timeout(&address, CONNECT_TIMEOUT) {
            Ok(stream) => {
                stream
                    .set_read_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                stream
                    .set_write_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                let mut session = Session::new().map_err(|_| NekoError::SshHandshakeFailed)?;
                session.set_timeout(SSH_OPERATION_TIMEOUT_MS);
                session.set_tcp_stream(stream);
                session
                    .handshake()
                    .map_err(|_| NekoError::SshHandshakeFailed)?;
                return Ok(session);
            }
            Err(_) => continue,
        }
    }
    Err(NekoError::SshConnectionFailed)
}

fn presented_host_key(session: &Session) -> Result<PresentedHostKey> {
    let (public_key, key_type) = session.host_key().ok_or(NekoError::SshHostKeyUnavailable)?;
    if public_key.is_empty() {
        return Err(NekoError::SshHostKeyUnavailable);
    }
    // DSA (`ssh-dss`) is obsolete and must never enter the trust flow, even when the bundled
    // libssh2 build can negotiate it.  Keep the remaining host-key types explicit so every key
    // that can be trusted is still fingerprinted and verified by the profile store.
    let key_algorithm = validated_host_key_algorithm(key_type)?;
    Ok(PresentedHostKey {
        key_algorithm: key_algorithm.to_owned(),
        public_key: public_key.to_vec(),
    })
}

fn host_key_algorithm(key_type: HostKeyType) -> Option<&'static str> {
    match key_type {
        HostKeyType::Rsa => Some("ssh-rsa"),
        // `ssh-dss` is deliberately rejected in `presented_host_key` before a key can be
        // displayed, persisted, or authenticated against.
        HostKeyType::Dss => None,
        HostKeyType::Ecdsa256 => Some("ecdsa-sha2-nistp256"),
        HostKeyType::Ecdsa384 => Some("ecdsa-sha2-nistp384"),
        HostKeyType::Ecdsa521 => Some("ecdsa-sha2-nistp521"),
        HostKeyType::Ed25519 => Some("ssh-ed25519"),
        HostKeyType::Unknown => None,
    }
}

fn validated_host_key_algorithm(key_type: HostKeyType) -> Result<&'static str> {
    match key_type {
        HostKeyType::Dss => Err(NekoError::SshHostKeyAlgorithmRejected),
        key_type => host_key_algorithm(key_type).ok_or(NekoError::SshHostKeyUnavailable),
    }
}

fn authenticate(session: &Session, profile: &SshProfile) -> Result<()> {
    match &profile.authentication {
        StoredAuthentication::Agent => session
            .userauth_agent(&profile.username)
            .map_err(|_| NekoError::SshAuthenticationFailed)?,
        StoredAuthentication::Password { password } => password
            .with_exposed(|password| session.userauth_password(&profile.username, password))
            .map_err(|_| NekoError::SshAuthenticationFailed)?,
        StoredAuthentication::PrivateKey {
            private_key_pem,
            passphrase,
        } => authenticate_private_key_memory(
            session,
            &profile.username,
            private_key_pem,
            passphrase.as_ref(),
        )?,
    };
    if session.authenticated() {
        Ok(())
    } else {
        Err(NekoError::SshAuthenticationFailed)
    }
}

fn authenticate_private_key_memory(
    session: &Session,
    username: &str,
    private_key_pem: &StoredSecret,
    passphrase: Option<&StoredSecret>,
) -> Result<()> {
    private_key_pem
        .with_exposed(|private_key_pem| match passphrase {
            Some(passphrase) => passphrase.with_exposed(|passphrase| {
                session.userauth_pubkey_memory(username, None, private_key_pem, Some(passphrase))
            }),
            None => session.userauth_pubkey_memory(username, None, private_key_pem, None),
        })
        .map_err(|_| NekoError::SshAuthenticationFailed)
}

fn read_channel_output(
    channel: &mut ssh2::Channel,
    close_signal: &ProfileCloseSignal,
) -> Result<(String, bool)> {
    let mut bytes = Vec::new();
    let mut buffer = [0_u8; 8 * 1024];
    let mut truncated = false;

    loop {
        if close_signal.is_closing() {
            let _ = channel.close();
            return Err(NekoError::ProfileNotFound);
        }
        if bytes.len() >= MAX_COMMAND_OUTPUT_BYTES {
            truncated = true;
            break;
        }
        let remaining = MAX_COMMAND_OUTPUT_BYTES - bytes.len();
        let chunk_len = remaining.min(buffer.len());
        let read = channel
            .read(&mut buffer[..chunk_len])
            .map_err(|_| NekoError::SshCommandFailed)?;
        if read == 0 {
            break;
        }
        bytes.extend_from_slice(&buffer[..read]);
    }

    Ok((String::from_utf8_lossy(&bytes).into_owned(), truncated))
}

fn validate_command(command: &str) -> Result<()> {
    if command.trim().is_empty()
        || command.len() > MAX_COMMAND_BYTES
        || command.as_bytes().contains(&0)
    {
        return Err(NekoError::SshCommandInvalid);
    }
    Ok(())
}

fn reserve_directory_metadata_bytes(current: usize, name: &str) -> Option<usize> {
    if name.len() > MAX_DIRECTORY_ENTRY_NAME_BYTES {
        return None;
    }
    current
        .checked_add(std::mem::size_of::<SshDirectoryEntry>())?
        .checked_add(name.len())
        .filter(|next| *next <= MAX_DIRECTORY_METADATA_BYTES)
}

/// Shared by narrow SFTP operations.  This checks only transport-safe path syntax; remote
/// authorization remains enforced by the authenticated SSH account and the SFTP server.
pub(crate) fn validate_remote_path(path: &str) -> Result<()> {
    if path.trim().is_empty()
        || path.len() > MAX_REMOTE_PATH_BYTES
        || path.as_bytes().contains(&0)
        || path.chars().any(char::is_control)
    {
        return Err(NekoError::SshDirectoryPathInvalid);
    }
    Ok(())
}

fn command_for_working_directory(command: String, cwd: Option<String>) -> Result<(String, String)> {
    validate_command(&command)?;
    let Some(cwd) = cwd else {
        return Ok((command, String::new()));
    };
    validate_remote_path(&cwd)?;
    let remote_command = format!("cd -- {} && {command}", shell_quote(&cwd));
    if remote_command.len() > MAX_COMMAND_BYTES {
        return Err(NekoError::SshCommandInvalid);
    }
    Ok((remote_command, cwd))
}

fn shell_quote(value: &str) -> String {
    format!("'{}'", value.replace('\'', "'\"'\"'"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn allows_modern_host_key_algorithms_and_excludes_dsa() {
        assert_eq!(
            host_key_algorithm(HostKeyType::Ed25519),
            Some("ssh-ed25519")
        );
        assert_eq!(
            host_key_algorithm(HostKeyType::Ecdsa256),
            Some("ecdsa-sha2-nistp256")
        );
        assert_eq!(host_key_algorithm(HostKeyType::Rsa), Some("ssh-rsa"));
        assert_eq!(host_key_algorithm(HostKeyType::Dss), None);
        assert_eq!(host_key_algorithm(HostKeyType::Unknown), None);
        assert!(matches!(
            validated_host_key_algorithm(HostKeyType::Dss),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
    }

    #[test]
    fn host_key_status_uses_the_frontend_camel_case_contract() {
        let serialized = serde_json::to_value(SshHostKeyStatus::ConfirmationRequired {
            confirmation_id: Uuid::nil(),
            host: "ssh.example.test".to_owned(),
            port: 22,
            key_algorithm: "ssh-ed25519".to_owned(),
            fingerprint: "SHA256:test".to_owned(),
        })
        .expect("status serializes");

        assert_eq!(serialized["status"], "confirmation_required");
        assert_eq!(serialized["confirmationId"], Uuid::nil().to_string());
        assert_eq!(serialized["keyAlgorithm"], "ssh-ed25519");
        assert!(serialized.get("confirmation_id").is_none());
    }

    #[test]
    fn pending_host_key_expires_before_confirmation() {
        let mut keys = PendingHostKeys::default();
        let confirmation_id = Uuid::new_v4();
        keys.entries.insert(
            confirmation_id,
            PendingHostKey {
                profile_id: Uuid::nil(),
                host: "ssh.example.test".to_owned(),
                port: 22,
                key_algorithm: "ssh-ed25519".to_owned(),
                public_key: vec![1, 2, 3],
                expires_at: Instant::now() - Duration::from_secs(1),
            },
        );

        assert!(matches!(
            keys.take(confirmation_id),
            Err(NekoError::SshHostKeyConfirmationExpired)
        ));
    }

    #[test]
    fn command_and_remote_path_validation_boundaries_are_strict() {
        assert!(validate_command("uname -a").is_ok());
        assert!(matches!(
            validate_command("\0"),
            Err(NekoError::SshCommandInvalid)
        ));
        assert!(validate_remote_path("/home/neko").is_ok());
        assert!(matches!(
            validate_remote_path("/home/\nnot-allowed"),
            Err(NekoError::SshDirectoryPathInvalid)
        ));
        let (remote_command, working_directory) =
            command_for_working_directory("pwd".to_owned(), Some("/tmp/a'b".to_owned()))
                .expect("valid quoted working directory");
        assert_eq!(remote_command, "cd -- '/tmp/a'\"'\"'b' && pwd");
        assert_eq!(working_directory, "/tmp/a'b");
    }

    #[test]
    fn directory_metadata_budget_bounds_names_and_aggregate_memory() {
        let normal_cost =
            reserve_directory_metadata_bytes(0, "report.csv").expect("normal entry fits");
        assert!(normal_cost > "report.csv".len());
        assert_eq!(
            reserve_directory_metadata_bytes(
                MAX_DIRECTORY_METADATA_BYTES - normal_cost,
                "report.csv"
            ),
            Some(MAX_DIRECTORY_METADATA_BYTES)
        );
        assert!(
            reserve_directory_metadata_bytes(
                MAX_DIRECTORY_METADATA_BYTES - normal_cost + 1,
                "report.csv"
            )
            .is_none()
        );
        assert!(
            reserve_directory_metadata_bytes(0, &"x".repeat(MAX_DIRECTORY_ENTRY_NAME_BYTES + 1))
                .is_none()
        );
    }
}
