//! Integrated, one-shot SSH operations.
//!
//! The webview passes only a saved profile id plus an explicit remote command or directory path.
//! Vaulted credentials never become command-line arguments, frontend values, or persistent session
//! state.  Each operation creates, verifies, authenticates, uses, and drops an in-process SSH
//! connection on a blocking worker thread.

use std::{
    collections::HashMap,
    io::Read,
    net::{TcpStream, ToSocketAddrs},
    path::Path,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

#[cfg(windows)]
use std::{
    ffi::c_void,
    fs::{self, OpenOptions},
    io::Write,
    os::windows::ffi::OsStrExt,
    path::PathBuf,
};

use serde::Serialize;
use ssh2::{ExtendedData, HostKeyType, Session};
use uuid::Uuid;

use crate::{
    domain::{HostKeyVerification, SshProfile, StoredAuthentication, StoredSecret},
    error::{Result, VaultError},
};

const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);
const SSH_OPERATION_TIMEOUT_MS: u32 = 20_000;
const PENDING_HOST_KEY_TTL: Duration = Duration::from_secs(5 * 60);
const MAX_COMMAND_BYTES: usize = 32 * 1024;
const MAX_COMMAND_OUTPUT_BYTES: usize = 1024 * 1024;
const MAX_REMOTE_PATH_BYTES: usize = 4 * 1024;
const MAX_DIRECTORY_ENTRIES: usize = 10_000;

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
/// Minimal source of a transient SSH profile and its host-key policy.  The desktop Vault and the
/// standalone browser server both implement this boundary.  The browser implementation keeps
/// every value in memory only; it never creates a Vault or writes a known-hosts file.
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
                .map_err(|_| VaultError::LockPoisoned)?;
            pending_host_keys.take(confirmation_id)?
        };
        if pending.profile_id != expected_profile_id {
            return Err(VaultError::SshHostKeyConfirmationExpired);
        }

        let profile = self.profiles.ssh_profile_snapshot(pending.profile_id)?;
        if profile.host != pending.host || profile.port != pending.port {
            return Err(VaultError::SshHostKeyConfirmationExpired);
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
            .map_err(|_| VaultError::LockPoisoned)?;
        pending_host_keys.remove(confirmation_id);
        Ok(())
    }

    /// Removes uncommitted host-key confirmations that belong to a disconnected ephemeral
    /// profile.
    pub fn forget_profile(&self, profile_id: Uuid) -> Result<()> {
        let mut pending_host_keys = self
            .pending_host_keys
            .lock()
            .map_err(|_| VaultError::LockPoisoned)?;
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
    ) -> Result<SshCommandResult> {
        let (command, working_directory) = command_for_working_directory(command, cwd)?;
        let session = self.open_authenticated_for_terminal(profile_id)?;
        let mut channel = session
            .channel_session()
            .map_err(|_| VaultError::SshCommandFailed)?;
        // Merge output before execution so a large stderr stream cannot block while stdout is
        // drained.  This is the most useful shape for the current terminal-style UI.
        channel
            .handle_extended_data(ExtendedData::Merge)
            .map_err(|_| VaultError::SshCommandFailed)?;
        channel
            .exec(&command)
            .map_err(|_| VaultError::SshCommandFailed)?;

        let (output, truncated) = read_channel_output(&mut channel)?;
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
            .map_err(|_| VaultError::SshDirectoryListFailed)?;
        let mut directory = sftp
            .opendir(Path::new(&path))
            .map_err(|_| VaultError::SshDirectoryListFailed)?;
        let mut entries = Vec::new();
        let mut truncated = false;

        loop {
            if entries.len() >= MAX_DIRECTORY_ENTRIES {
                truncated = true;
                break;
            }
            match directory.readdir() {
                Ok((entry_path, stat)) => {
                    let name = entry_path.file_name().unwrap_or(entry_path.as_os_str());
                    let name = name.to_string_lossy().into_owned();
                    if name == "." || name == ".." {
                        continue;
                    }
                    entries.push(SshDirectoryEntry {
                        name,
                        is_directory: stat.is_dir(),
                        size: stat.size,
                        modified_at_unix: stat.mtime,
                    });
                }
                // ssh2's low-level File::readdir deliberately uses this exact local error to
                // signal a normal end-of-directory condition.
                Err(error) if error.message() == "no more files" => break,
                Err(_) => return Err(VaultError::SshDirectoryListFailed),
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
            .map_err(|_| VaultError::LockPoisoned)?;
        Ok(pending_host_keys.insert(pending))
    }

    /// Opens a fresh transport only after the encrypted-vault known-hosts policy accepts the
    /// presented key.  Kept crate-private so the PTY service can use the same verification and
    /// authentication path as one-shot command/SFTP operations without exposing credentials.
    pub(crate) fn open_authenticated_for_terminal(&self, profile_id: Uuid) -> Result<Session> {
        let profile = self.profiles.ssh_profile_snapshot(profile_id)?;
        self.open_authenticated_snapshot(&profile)
    }

    /// Uses a caller-owned profile snapshot so a security-sensitive operation can pin the exact
    /// password-authenticated identity it is about to update.  The snapshot never crosses IPC and
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
            HostKeyVerification::Unknown { .. } => return Err(VaultError::SshHostKeyUntrusted),
            HostKeyVerification::Mismatch { .. } => return Err(VaultError::SshHostKeyMismatch),
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
    fn insert(&mut self, pending: PendingHostKey) -> Uuid {
        self.purge_expired();
        let confirmation_id = Uuid::new_v4();
        self.entries.insert(confirmation_id, pending);
        confirmation_id
    }

    fn take(&mut self, confirmation_id: Uuid) -> Result<PendingHostKey> {
        self.purge_expired();
        self.entries
            .remove(&confirmation_id)
            .ok_or(VaultError::SshHostKeyConfirmationExpired)
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
        .map_err(|_| VaultError::SshConnectionFailed)?;
    for address in addresses {
        match TcpStream::connect_timeout(&address, CONNECT_TIMEOUT) {
            Ok(stream) => {
                stream
                    .set_read_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| VaultError::SshConnectionFailed)?;
                stream
                    .set_write_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| VaultError::SshConnectionFailed)?;
                let mut session = Session::new().map_err(|_| VaultError::SshHandshakeFailed)?;
                session.set_timeout(SSH_OPERATION_TIMEOUT_MS);
                session.set_tcp_stream(stream);
                session
                    .handshake()
                    .map_err(|_| VaultError::SshHandshakeFailed)?;
                return Ok(session);
            }
            Err(_) => continue,
        }
    }
    Err(VaultError::SshConnectionFailed)
}

fn presented_host_key(session: &Session) -> Result<PresentedHostKey> {
    let (public_key, key_type) = session
        .host_key()
        .ok_or(VaultError::SshHostKeyUnavailable)?;
    if public_key.is_empty() {
        return Err(VaultError::SshHostKeyUnavailable);
    }
    let key_algorithm = host_key_algorithm(key_type).ok_or(VaultError::SshHostKeyUnavailable)?;
    Ok(PresentedHostKey {
        key_algorithm: key_algorithm.to_owned(),
        public_key: public_key.to_vec(),
    })
}

fn host_key_algorithm(key_type: HostKeyType) -> Option<&'static str> {
    match key_type {
        HostKeyType::Rsa => Some("ssh-rsa"),
        HostKeyType::Dss => Some("ssh-dss"),
        HostKeyType::Ecdsa256 => Some("ecdsa-sha2-nistp256"),
        HostKeyType::Ecdsa384 => Some("ecdsa-sha2-nistp384"),
        HostKeyType::Ecdsa521 => Some("ecdsa-sha2-nistp521"),
        HostKeyType::Ed25519 => Some("ssh-ed25519"),
        HostKeyType::Unknown => None,
    }
}

fn authenticate(session: &Session, profile: &SshProfile) -> Result<()> {
    match &profile.authentication {
        StoredAuthentication::Agent => session
            .userauth_agent(&profile.username)
            .map_err(|_| VaultError::SshAuthenticationFailed)?,
        StoredAuthentication::Password { password } => password
            .with_exposed(|password| session.userauth_password(&profile.username, password))
            .map_err(|_| VaultError::SshAuthenticationFailed)?,
        StoredAuthentication::PrivateKey {
            private_key_pem,
            passphrase,
        } => authenticate_private_key_memory(
            session,
            &profile.username,
            private_key_pem,
            passphrase.as_ref(),
        )?,
        StoredAuthentication::PrivateKeyFile { private_key_path } => session
            .userauth_pubkey_file(&profile.username, None, Path::new(private_key_path), None)
            .map_err(|_| VaultError::SshAuthenticationFailed)?,
    };
    if session.authenticated() {
        Ok(())
    } else {
        Err(VaultError::SshAuthenticationFailed)
    }
}

#[cfg(unix)]
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
        .map_err(|_| VaultError::SshAuthenticationFailed)
}

#[cfg(windows)]
fn authenticate_private_key_memory(
    session: &Session,
    username: &str,
    private_key_pem: &StoredSecret,
    passphrase: Option<&StoredSecret>,
) -> Result<()> {
    private_key_pem.with_exposed(|private_key_pem| {
        // Windows CNG's libssh2 backend accepts a key file but does not expose the in-memory PEM
        // API.  The Vault remains the only persistent location: this ACL-restricted random temp
        // file is created only after an authenticated transport exists and is deleted immediately
        // after libssh2 has synchronously parsed it.
        let temporary_key = TemporaryPrivateKeyFile::create(private_key_pem)?;
        let authentication_result = match passphrase {
            Some(passphrase) => passphrase.with_exposed(|passphrase| {
                session.userauth_pubkey_file(username, None, temporary_key.path(), Some(passphrase))
            }),
            None => session.userauth_pubkey_file(username, None, temporary_key.path(), None),
        };
        let cleanup_result = temporary_key.remove();
        if cleanup_result.is_err() {
            return Err(VaultError::SshTemporaryKeyCleanupFailed);
        }
        authentication_result.map_err(|_| VaultError::SshAuthenticationFailed)
    })
}

#[cfg(not(any(unix, windows)))]
fn authenticate_private_key_memory(
    _session: &Session,
    _username: &str,
    _private_key_pem: &StoredSecret,
    _passphrase: Option<&StoredSecret>,
) -> Result<()> {
    // Keep an explicit rejection for an unsupported target rather than writing a private key to a
    // long-lived local file.
    Err(VaultError::SshAuthenticationUnsupported)
}

#[cfg(windows)]
struct TemporaryPrivateKeyFile {
    path: Option<PathBuf>,
}

#[cfg(windows)]
impl TemporaryPrivateKeyFile {
    fn create(private_key_pem: &str) -> Result<Self> {
        let temporary_directory = std::env::temp_dir();
        for _ in 0..8 {
            let path = temporary_directory.join(format!("nekossh-key-{}.pem", Uuid::new_v4()));
            let mut file = match OpenOptions::new().write(true).create_new(true).open(&path) {
                Ok(file) => file,
                Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
                Err(_) => return Err(VaultError::SshTemporaryKeyFileFailed),
            };

            // Restrict the empty file before a private byte is written.  A protected DACL grants
            // full control only to its owner and LocalSystem; administrators cannot inherit an
            // ambient temp-directory ACL into this short-lived credential file.
            if restrict_windows_temp_key_acl(&path).is_err() {
                drop(file);
                let _ = fs::remove_file(&path);
                return Err(VaultError::SshTemporaryKeyFileFailed);
            }
            let write_result = file
                .write_all(private_key_pem.as_bytes())
                .and_then(|_| file.sync_all());
            drop(file);
            if write_result.is_err() {
                let _ = fs::remove_file(&path);
                return Err(VaultError::SshTemporaryKeyFileFailed);
            }
            return Ok(Self { path: Some(path) });
        }
        Err(VaultError::SshTemporaryKeyFileFailed)
    }

    fn path(&self) -> &Path {
        self.path
            .as_deref()
            .expect("temporary private-key path is available before cleanup")
    }

    fn remove(mut self) -> std::io::Result<()> {
        let Some(path) = self.path.take() else {
            return Ok(());
        };
        match fs::remove_file(&path) {
            Ok(()) => Ok(()),
            Err(error) => {
                // Keep the path owned so Drop gets one immediate best-effort retry while this
                // stack frame unwinds (for example, if an antivirus briefly held the file).
                self.path = Some(path);
                Err(error)
            }
        }
    }
}

#[cfg(windows)]
impl Drop for TemporaryPrivateKeyFile {
    fn drop(&mut self) {
        if let Some(path) = self.path.take() {
            let _ = fs::remove_file(path);
        }
    }
}

/// Applies a protected Windows DACL to a freshly-created, still-empty temporary key file.  `OW`
/// resolves to that file's owner, and `SY` preserves LocalSystem access needed by normal system
/// services.  The descriptor is process-local and is released with `LocalFree` immediately.
#[cfg(windows)]
fn restrict_windows_temp_key_acl(path: &Path) -> std::io::Result<()> {
    const SDDL_REVISION_1: u32 = 1;
    const DACL_SECURITY_INFORMATION: u32 = 0x0000_0004;
    const PROTECTED_DACL_SECURITY_INFORMATION: u32 = 0x8000_0000;
    let descriptor_text: Vec<u16> = "D:P(A;;FA;;;OW)(A;;FA;;;SY)"
        .encode_utf16()
        .chain(std::iter::once(0))
        .collect();
    let path: Vec<u16> = path
        .as_os_str()
        .encode_wide()
        .chain(std::iter::once(0))
        .collect();
    let mut descriptor = std::ptr::null_mut::<c_void>();
    if unsafe {
        ConvertStringSecurityDescriptorToSecurityDescriptorW(
            descriptor_text.as_ptr(),
            SDDL_REVISION_1,
            &mut descriptor,
            std::ptr::null_mut(),
        )
    } == 0
    {
        return Err(std::io::Error::last_os_error());
    }
    let applied = unsafe {
        SetFileSecurityW(
            path.as_ptr(),
            DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
            descriptor,
        )
    };
    unsafe {
        let _ = LocalFree(descriptor);
    }
    if applied == 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

#[cfg(windows)]
#[link(name = "advapi32")]
unsafe extern "system" {
    fn ConvertStringSecurityDescriptorToSecurityDescriptorW(
        string_security_descriptor: *const u16,
        string_sd_revision: u32,
        security_descriptor: *mut *mut c_void,
        security_descriptor_size: *mut u32,
    ) -> i32;
    fn SetFileSecurityW(
        file_name: *const u16,
        security_information: u32,
        security_descriptor: *const c_void,
    ) -> i32;
}

#[cfg(windows)]
#[link(name = "kernel32")]
unsafe extern "system" {
    fn LocalFree(memory: *mut c_void) -> *mut c_void;
}

fn read_channel_output(channel: &mut ssh2::Channel) -> Result<(String, bool)> {
    let mut bytes = Vec::new();
    let mut buffer = [0_u8; 8 * 1024];
    let mut truncated = false;

    loop {
        if bytes.len() >= MAX_COMMAND_OUTPUT_BYTES {
            truncated = true;
            break;
        }
        let remaining = MAX_COMMAND_OUTPUT_BYTES - bytes.len();
        let chunk_len = remaining.min(buffer.len());
        let read = channel
            .read(&mut buffer[..chunk_len])
            .map_err(|_| VaultError::SshCommandFailed)?;
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
        return Err(VaultError::SshCommandInvalid);
    }
    Ok(())
}

/// Shared by narrow SFTP operations.  This checks only transport-safe path syntax; remote
/// authorization remains enforced by the authenticated SSH account and the SFTP server.
pub(crate) fn validate_remote_path(path: &str) -> Result<()> {
    if path.trim().is_empty()
        || path.len() > MAX_REMOTE_PATH_BYTES
        || path.as_bytes().contains(&0)
        || path.chars().any(char::is_control)
    {
        return Err(VaultError::SshDirectoryPathInvalid);
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
        return Err(VaultError::SshCommandInvalid);
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
    fn maps_supported_host_key_algorithms() {
        assert_eq!(
            host_key_algorithm(HostKeyType::Ed25519),
            Some("ssh-ed25519")
        );
        assert_eq!(
            host_key_algorithm(HostKeyType::Ecdsa256),
            Some("ecdsa-sha2-nistp256")
        );
        assert_eq!(host_key_algorithm(HostKeyType::Unknown), None);
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
            Err(VaultError::SshHostKeyConfirmationExpired)
        ));
    }

    #[test]
    fn command_and_remote_path_validation_boundaries_are_strict() {
        assert!(validate_command("uname -a").is_ok());
        assert!(matches!(
            validate_command("\0"),
            Err(VaultError::SshCommandInvalid)
        ));
        assert!(validate_remote_path("/home/neko").is_ok());
        assert!(matches!(
            validate_remote_path("/home/\nnot-allowed"),
            Err(VaultError::SshDirectoryPathInvalid)
        ));
        let (remote_command, working_directory) =
            command_for_working_directory("pwd".to_owned(), Some("/tmp/a'b".to_owned()))
                .expect("valid quoted working directory");
        assert_eq!(remote_command, "cd -- '/tmp/a'\"'\"'b' && pwd");
        assert_eq!(working_directory, "/tmp/a'b");
    }
}
