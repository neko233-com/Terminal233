//! Integrated, one-shot SSH operations.
//!
//! The webview passes only a saved profile id plus an explicit remote command or directory path.
//! Transient credentials never become command-line arguments or persistent session state. Each
//! operation creates, verifies, authenticates, uses, and drops an in-process SSH
//! connection on a blocking worker thread.

use std::{
    collections::HashMap,
    net::{TcpStream, ToSocketAddrs},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use puressh::{
    client::{AlgoOverrides, Client, Config, HostKeyPolicy, KnownHostsPolicy, TofuAction},
    key::PrivateKey,
    known_hosts::KnownHosts,
};
use serde::Serialize;
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
const MIN_RSA_HOST_KEY_BITS: usize = 2048;
const MODERN_KEX_ALGORITHMS: &[&str] = &[
    "mlkem768x25519-sha256",
    "curve25519-sha256",
    "curve25519-sha256@libssh.org",
    "ecdh-sha2-nistp521",
    "ecdh-sha2-nistp384",
    "ecdh-sha2-nistp256",
    "diffie-hellman-group14-sha256",
];
const MODERN_HOST_KEY_ALGORITHMS: &[&str] = &[
    "ssh-ed25519",
    "ecdsa-sha2-nistp521",
    "ecdsa-sha2-nistp384",
    "ecdsa-sha2-nistp256",
    "rsa-sha2-512",
    "rsa-sha2-256",
];
const MODERN_CIPHER_ALGORITHMS: &[&str] = &[
    "chacha20-poly1305@openssh.com",
    "aes256-gcm@openssh.com",
    "aes128-gcm@openssh.com",
    "aes256-ctr",
    "aes192-ctr",
    "aes128-ctr",
];
#[cfg(test)]
const MODERN_AEAD_CIPHER_ALGORITHMS: &[&str] =
    &["aes256-gcm@openssh.com", "aes128-gcm@openssh.com"];
const MODERN_MAC_ALGORITHMS: &[&str] = &[
    "hmac-sha2-256-etm@openssh.com",
    "hmac-sha2-512-etm@openssh.com",
    "hmac-sha2-256",
    "hmac-sha2-512",
];

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

    /// Opens a fresh transport and verifies its host key. A previously trusted key is followed by
    /// an immediate credential probe on that same verified transport, so the renderer cannot
    /// report a connected workspace before authentication has actually succeeded. Unknown keys
    /// remain unauthenticated until the caller explicitly approves the displayed fingerprint.
    pub fn prepare(&self, profile_id: Uuid) -> Result<SshHostKeyStatus> {
        let profile = self.profiles.ssh_profile_snapshot(profile_id)?;
        let (mut session, presented) = connect_and_handshake(&profile)?;

        match self.profiles.verify_host_key(
            profile_id,
            &profile.host,
            profile.port,
            &presented.key_algorithm,
            &presented.public_key,
        )? {
            HostKeyVerification::Trusted { fingerprint } => {
                authenticate(&mut session.client, &profile)?;
                Ok(SshHostKeyStatus::Trusted {
                    host: profile.host,
                    port: profile.port,
                    key_algorithm: presented.key_algorithm,
                    fingerprint,
                })
            }
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
    /// webview. Before reporting success, a fresh connection handshakes, re-verifies the stored
    /// key and authenticates the profile. This closes the key-change window between confirmation
    /// and entering the workspace, and keeps credential failures in the connection progress UI.
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
        let authenticated = self.open_authenticated_snapshot(&profile)?;
        drop(authenticated);

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
        let mut session = self.open_authenticated_for_terminal(profile_id)?;
        // Host resolution, handshake, host-key verification, authentication, and channel setup
        // may all outlive the command's initial admission. Linearize immediately before `exec`:
        // if disconnect closed the profile first, no remote command is issued.
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        let executed = session
            .client
            .exec(&command)
            .map_err(|_| NekoError::SshCommandFailed)?;

        // puressh drains both SSH data streams while processing packets, so a peer cannot
        // deadlock the command by filling stderr. The UI consumes one terminal-style buffer;
        // retain stdout first and then the separately captured stderr within the IPC cap.
        let mut output_bytes = executed.stdout;
        output_bytes.extend_from_slice(&executed.stderr);
        let truncated = output_bytes.len() > MAX_COMMAND_OUTPUT_BYTES;
        output_bytes.truncate(MAX_COMMAND_OUTPUT_BYTES);
        let output = String::from_utf8_lossy(&output_bytes).into_owned();
        let exit_status = executed
            .exit_status
            .and_then(|status| i32::try_from(status).ok())
            .unwrap_or(-1);

        Ok(SshCommandResult {
            output,
            exit_status,
            working_directory,
            truncated,
        })
    }

    #[allow(deprecated)]
    pub fn list_directory(&self, profile_id: Uuid, path: String) -> Result<SshDirectoryListing> {
        validate_remote_path(&path)?;
        let mut session = self.open_authenticated_for_terminal(profile_id)?;
        let mut sftp = session
            .client
            .sftp()
            .map_err(|_| NekoError::SshDirectoryListFailed)?;
        let directory = sftp
            .opendir(path.as_bytes())
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
            let batch = match sftp.readdir(&directory) {
                Ok(Some(batch)) => batch,
                Ok(None) => break,
                Err(_) => {
                    let _ = sftp.close(&directory);
                    return Err(NekoError::SshDirectoryListFailed);
                }
            };
            for entry in batch {
                if entries_seen >= MAX_DIRECTORY_ENTRIES {
                    truncated = true;
                    break;
                }
                entries_seen += 1;
                if entry.filename.len() > MAX_DIRECTORY_ENTRY_NAME_BYTES {
                    truncated = true;
                    continue;
                }
                let name = String::from_utf8_lossy(&entry.filename).into_owned();
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
                    is_directory: entry.attrs.is_dir(),
                    size: entry.attrs.size,
                    modified_at_unix: entry
                        .attrs
                        .atime_mtime
                        .map(|(_, modified)| u64::from(modified)),
                });
            }
        }
        sftp.close(&directory)
            .map_err(|_| NekoError::SshDirectoryListFailed)?;

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
    pub(crate) fn open_authenticated_for_terminal(
        &self,
        profile_id: Uuid,
    ) -> Result<NativeSshSession> {
        let profile = self.profiles.ssh_profile_snapshot(profile_id)?;
        self.open_authenticated_snapshot(&profile)
    }

    /// Uses a caller-owned profile snapshot so a security-sensitive operation can pin the exact
    /// authenticated identity it is about to use. The snapshot never crosses IPC and
    /// is dropped (and its stored secrets zeroized) once the operation completes.
    pub(crate) fn open_authenticated_snapshot(
        &self,
        profile: &SshProfile,
    ) -> Result<NativeSshSession> {
        let (mut session, presented) = connect_and_handshake(profile)?;
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
        authenticate(&mut session.client, profile)?;
        Ok(session)
    }
}

pub(crate) struct NativeSshSession {
    pub(crate) client: Client,
    shutdown_stream: TcpStream,
}

impl NativeSshSession {
    pub(crate) fn into_parts(self) -> (Client, TcpStream) {
        (self.client, self.shutdown_stream)
    }
}

#[derive(Clone)]
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

fn connect_and_handshake(profile: &SshProfile) -> Result<(NativeSshSession, PresentedHostKey)> {
    let addresses = (profile.host.as_str(), profile.port)
        .to_socket_addrs()
        .map_err(|_| NekoError::SshConnectionFailed)?;
    let mut reached_peer = false;
    for address in addresses {
        match TcpStream::connect_timeout(&address, CONNECT_TIMEOUT) {
            Ok(stream) => {
                reached_peer = true;
                stream
                    .set_read_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                stream
                    .set_write_timeout(Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())))
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                stream
                    .set_nodelay(true)
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                let shutdown_stream = stream
                    .try_clone()
                    .map_err(|_| NekoError::SshConnectionFailed)?;
                let captured = Arc::new(Mutex::new(None::<PresentedHostKey>));
                let callback_capture = Arc::clone(&captured);
                let prompt = Arc::new(move |prompt: &puressh::client::HostKeyPrompt<'_>| {
                    let presented = PresentedHostKey {
                        key_algorithm: prompt.key_type.to_owned(),
                        public_key: prompt.key_blob.to_vec(),
                    };
                    let accepted = validate_presented_host_key(&presented).is_ok();
                    if let Ok(mut capture) = callback_capture.lock() {
                        *capture = Some(presented);
                    }
                    accepted
                });
                let config = Config {
                    host_key_policy: HostKeyPolicy::KnownHosts(KnownHostsPolicy {
                        store: Arc::new(Mutex::new(KnownHosts::new())),
                        save_path: None,
                        hash_new: false,
                        on_unknown: TofuAction::PromptDetailed(prompt),
                        on_mismatch: TofuAction::Reject,
                    }),
                    timeout: Some(Duration::from_millis(SSH_OPERATION_TIMEOUT_MS.into())),
                    algorithms: modern_algorithm_overrides(),
                };
                let connected =
                    Client::connect_via(Box::new(stream), &profile.host, profile.port, config);
                let presented = captured
                    .lock()
                    .map_err(|_| NekoError::LockPoisoned)?
                    .clone();
                let client = match connected {
                    Ok(client) => client,
                    Err(_) => {
                        if let Some(presented) = presented {
                            validate_presented_host_key(&presented)?;
                        }
                        continue;
                    }
                };
                let presented = presented.ok_or(NekoError::SshHostKeyUnavailable)?;
                validate_presented_host_key(&presented)?;
                return Ok((
                    NativeSshSession {
                        client,
                        shutdown_stream,
                    },
                    presented,
                ));
            }
            Err(_) => continue,
        }
    }
    Err(if reached_peer {
        NekoError::SshHandshakeFailed
    } else {
        NekoError::SshConnectionFailed
    })
}

fn modern_algorithm_overrides() -> AlgoOverrides {
    let strings = |values: &[&str]| {
        Some(
            values
                .iter()
                .map(|value| (*value).to_owned())
                .collect::<Vec<_>>(),
        )
    };
    AlgoOverrides {
        ciphers: strings(MODERN_CIPHER_ALGORITHMS),
        macs: strings(MODERN_MAC_ALGORITHMS),
        kex_algorithms: strings(MODERN_KEX_ALGORITHMS),
        host_key_algorithms: strings(MODERN_HOST_KEY_ALGORITHMS),
        pubkey_accepted_algorithms: strings(MODERN_HOST_KEY_ALGORITHMS),
        ca_signature_algorithms: strings(MODERN_HOST_KEY_ALGORITHMS),
        compression: Some(false),
    }
}

#[cfg(test)]
fn negotiated_transport_algorithms_are_allowed(cipher: &str, mac: Option<&str>) -> bool {
    if !MODERN_CIPHER_ALGORITHMS.contains(&cipher) {
        return false;
    }
    if MODERN_AEAD_CIPHER_ALGORITHMS.contains(&cipher) {
        true
    } else {
        mac.is_some_and(|mac| MODERN_MAC_ALGORITHMS.contains(&mac))
    }
}

fn validate_presented_host_key(presented: &PresentedHostKey) -> Result<()> {
    validated_host_key_algorithm(&presented.key_algorithm)?;
    validate_host_key_strength(&presented.key_algorithm, &presented.public_key)
}

fn validated_host_key_algorithm(negotiated_algorithm: &str) -> Result<&str> {
    if MODERN_HOST_KEY_ALGORITHMS.contains(&negotiated_algorithm) {
        Ok(negotiated_algorithm)
    } else {
        Err(NekoError::SshHostKeyAlgorithmRejected)
    }
}

fn validate_host_key_strength(key_algorithm: &str, public_key: &[u8]) -> Result<()> {
    if public_key.is_empty() {
        return Err(NekoError::SshHostKeyUnavailable);
    }
    if !matches!(key_algorithm, "rsa-sha2-512" | "rsa-sha2-256") {
        return Ok(());
    }
    let mut cursor = public_key;
    let algorithm = read_ssh_field(&mut cursor)?;
    let exponent = read_ssh_field(&mut cursor)?;
    let modulus = read_ssh_field(&mut cursor)?;
    if algorithm != b"ssh-rsa" || !cursor.is_empty() {
        return Err(NekoError::SshHostKeyUnavailable);
    }
    if exponent.strip_prefix(&[0]).unwrap_or(exponent) != [0x01, 0x00, 0x01] {
        return Err(NekoError::SshHostKeyAlgorithmRejected);
    }
    let modulus = modulus.strip_prefix(&[0]).unwrap_or(modulus);
    let Some(first) = modulus.first().copied() else {
        return Err(NekoError::SshHostKeyAlgorithmRejected);
    };
    let modulus_bits =
        (modulus.len() - 1) * 8 + (u8::BITS as usize - first.leading_zeros() as usize);
    if modulus_bits < MIN_RSA_HOST_KEY_BITS {
        Err(NekoError::SshHostKeyAlgorithmRejected)
    } else {
        Ok(())
    }
}

fn read_ssh_field<'a>(input: &mut &'a [u8]) -> Result<&'a [u8]> {
    let length_bytes = input.get(..4).ok_or(NekoError::SshHostKeyUnavailable)?;
    let length = u32::from_be_bytes(
        length_bytes
            .try_into()
            .map_err(|_| NekoError::SshHostKeyUnavailable)?,
    ) as usize;
    let end = 4_usize
        .checked_add(length)
        .ok_or(NekoError::SshHostKeyUnavailable)?;
    let value = input.get(4..end).ok_or(NekoError::SshHostKeyUnavailable)?;
    *input = input.get(end..).ok_or(NekoError::SshHostKeyUnavailable)?;
    Ok(value)
}

fn authenticate(session: &mut Client, profile: &SshProfile) -> Result<()> {
    match &profile.authentication {
        StoredAuthentication::Password { password } => password
            .with_exposed(|password| session.authenticate_password(&profile.username, password))
            .map_err(map_authentication_error)?,
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
    Ok(())
}

fn authenticate_private_key_memory(
    session: &mut Client,
    username: &str,
    private_key_pem: &StoredSecret,
    passphrase: Option<&StoredSecret>,
) -> Result<()> {
    let parsed = private_key_pem.with_exposed(|private_key_pem| match passphrase {
        Some(passphrase) => passphrase.with_exposed(|passphrase| {
            PrivateKey::parse_pem(private_key_pem, Some(passphrase.as_bytes()))
        }),
        None => PrivateKey::parse_pem(private_key_pem, None),
    });
    let signer = parsed
        .and_then(PrivateKey::into_host_key)
        .map_err(|_| NekoError::SshAuthenticationFailed)?;
    session
        .authenticate_publickey(username, signer)
        .map_err(map_authentication_error)
}

fn map_authentication_error(_error: puressh::Error) -> NekoError {
    #[cfg(test)]
    if std::env::var("NEKOSSH_INTEROP_DEBUG").as_deref() == Ok("1") {
        eprintln!("puressh authentication error: {_error:?}");
    }
    NekoError::SshAuthenticationFailed
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
    use crate::ipc::Channel as IpcChannel;
    use crate::{
        domain::{ProfileAuthenticationInput, SshProfileInput, TerminalPreferences},
        ephemeral::EphemeralConnectionStore,
        operation_admission::OperationAdmission,
        sftp_transfer::{SftpTransferService, SshSftpDownloadInput, SshSftpUploadInput},
        ssh_terminal::TerminalService,
    };
    use base64::{Engine as _, engine::general_purpose::STANDARD};
    use serde_json::Value;
    use std::sync::mpsc::{self, Receiver};

    const REMOTE_INTEROP_DIRECTORY: &str = "/tmp/terminal233-remote-interop-v1";
    const REMOTE_INTEROP_FILE: &str = "payload.bin";
    const REMOTE_INTEROP_RENAMED_FILE: &str = "renamed.bin";

    fn remote_interop_enabled() -> bool {
        std::env::var("TERMINAL233_REMOTE_SSH_INTEROP").as_deref() == Ok("1")
    }

    fn remote_interop_env(name: &str) -> String {
        std::env::var(name).unwrap_or_else(|_| panic!("missing remote SSH test variable: {name}"))
    }

    fn remote_interop_profile() -> SshProfileInput {
        let port = remote_interop_env("TERMINAL233_REMOTE_SSH_PORT")
            .parse::<u16>()
            .expect("remote SSH test port must be a valid u16");
        SshProfileInput {
            label: "aliyun-sg-remote-interoperability".to_owned(),
            host: remote_interop_env("TERMINAL233_REMOTE_SSH_HOST"),
            port,
            username: remote_interop_env("TERMINAL233_REMOTE_SSH_USERNAME"),
            authentication: ProfileAuthenticationInput::Password {
                password: remote_interop_env("TERMINAL233_REMOTE_SSH_PASSWORD"),
            },
            tags: vec!["remote-interoperability".to_owned()],
            terminal: TerminalPreferences::default(),
        }
    }

    fn remote_interop_command(
        ssh: &SshService,
        admission: &OperationAdmission,
        profile_id: Uuid,
        command: &str,
    ) -> SshCommandResult {
        let permit = admission
            .try_acquire(profile_id)
            .expect("admit remote SSH command");
        ssh.execute(profile_id, command.to_owned(), None, permit.close_signal())
            .expect("execute remote SSH command")
    }

    fn remote_interop_delete(
        service: &SftpTransferService,
        admission: &OperationAdmission,
        profile_id: Uuid,
        path: &str,
    ) {
        let permit = admission
            .try_acquire(profile_id)
            .expect("admit remote SSH cleanup");
        service
            .delete_remote(profile_id, path.to_owned(), permit.close_signal())
            .expect("delete remote SSH test entry");
    }

    fn remote_interop_safe_entry(name: &str) -> bool {
        name == REMOTE_INTEROP_FILE
            || name == REMOTE_INTEROP_RENAMED_FILE
            || (name.starts_with(".payload.bin.terminal233-") && name.ends_with(".part"))
    }

    fn clean_remote_interop_directory(
        ssh: &SshService,
        service: &SftpTransferService,
        admission: &OperationAdmission,
        profile_id: Uuid,
        remove_directory: bool,
    ) {
        let listing = match ssh.list_directory(profile_id, REMOTE_INTEROP_DIRECTORY.to_owned()) {
            Ok(listing) => listing,
            Err(_) => return,
        };
        for entry in listing.entries {
            assert!(
                remote_interop_safe_entry(&entry.name),
                "refusing to delete unexpected remote interop entry: {}",
                entry.name
            );
            remote_interop_delete(
                service,
                admission,
                profile_id,
                &format!("{REMOTE_INTEROP_DIRECTORY}/{}", entry.name),
            );
        }
        if remove_directory {
            remote_interop_delete(service, admission, profile_id, REMOTE_INTEROP_DIRECTORY);
        }
    }

    struct RemoteInteropCleanup {
        ssh: Arc<SshService>,
        service: Arc<SftpTransferService>,
        terminal: Arc<TerminalService>,
        admission: OperationAdmission,
        profile_id: Uuid,
        armed: bool,
    }

    impl RemoteInteropCleanup {
        fn run(&mut self) {
            if !self.armed {
                return;
            }
            let _ = self.terminal.close_for_profile(self.profile_id);
            let _ = self.service.cancel_for_profile(self.profile_id);
            std::thread::sleep(Duration::from_millis(250));
            clean_remote_interop_directory(
                &self.ssh,
                &self.service,
                &self.admission,
                self.profile_id,
                true,
            );
            self.armed = false;
        }
    }

    impl Drop for RemoteInteropCleanup {
        fn drop(&mut self) {
            self.run();
        }
    }

    fn interoperability_profile(
        host: String,
        port: u16,
        username: String,
        authentication: ProfileAuthenticationInput,
        label: &str,
    ) -> SshProfileInput {
        SshProfileInput {
            label: label.to_owned(),
            host,
            port,
            username,
            authentication,
            tags: vec!["interoperability".to_owned()],
            terminal: TerminalPreferences::default(),
        }
    }

    fn trust_fixture_host(ssh: &SshService, profile_id: Uuid) {
        let trace = |message: &str| {
            if std::env::var("NEKOSSH_INTEROP_PROGRESS").as_deref() == Ok("1") {
                eprintln!("native-interop trust: {message}");
            }
        };
        trace("prepare start");
        let confirmation_id = match ssh.prepare(profile_id).expect("prepare fixture host") {
            SshHostKeyStatus::ConfirmationRequired {
                confirmation_id, ..
            } => confirmation_id,
            unexpected => panic!("fixture host must require confirmation: {unexpected:?}"),
        };
        trace("prepare complete");
        trace("trust/authenticate start");
        assert!(matches!(
            ssh.trust_host_key(profile_id, confirmation_id)
                .expect("trust and authenticate fixture host"),
            SshHostKeyStatus::Trusted { .. }
        ));
        trace("trust/authenticate complete");
    }

    fn execute_fixture_command(
        ssh: &SshService,
        admission: &OperationAdmission,
        profile_id: Uuid,
        command: &str,
    ) -> SshCommandResult {
        let permit = admission
            .try_acquire(profile_id)
            .expect("admit fixture command");
        ssh.execute(
            profile_id,
            command.to_owned(),
            Some("/workspace".to_owned()),
            permit.close_signal(),
        )
        .expect("execute fixture command")
    }

    fn receive_transfer_events(receiver: &Receiver<Value>) -> Vec<Value> {
        let deadline = Instant::now() + Duration::from_secs(30);
        let mut events = Vec::new();
        loop {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .expect("transfer must finish before the integration-test deadline");
            let event = receiver
                .recv_timeout(remaining)
                .expect("receive transfer IPC event");
            let completed = event["kind"] == "completed";
            events.push(event);
            if completed {
                return events;
            }
        }
    }

    /// Runs only through `scripts/test-native-ssh-interoperability.mjs`. The Node process is a
    /// loopback SSH/SFTP fixture server; this side remains the production in-process pure-Rust
    /// puressh
    /// implementation and never invokes an external SSH client.
    #[test]
    fn native_ssh_interoperability() {
        if std::env::var("NEKOSSH_NATIVE_INTEROP").as_deref() != Ok("1") {
            return;
        }
        let progress = |message: &str| {
            if std::env::var("NEKOSSH_INTEROP_PROGRESS").as_deref() == Ok("1") {
                eprintln!("native-interop: {message}");
            }
        };

        let host = std::env::var("NEKOSSH_NATIVE_INTEROP_HOST").expect("fixture host");
        let port = std::env::var("NEKOSSH_NATIVE_INTEROP_PORT")
            .expect("fixture port")
            .parse::<u16>()
            .expect("valid fixture port");
        let username = std::env::var("NEKOSSH_NATIVE_INTEROP_USERNAME").expect("fixture username");
        let password = std::env::var("NEKOSSH_NATIVE_INTEROP_PASSWORD").expect("fixture password");
        let private_key_path =
            std::env::var("NEKOSSH_NATIVE_INTEROP_PRIVATE_KEY_PATH").expect("fixture key path");
        let private_key_passphrase = std::env::var("NEKOSSH_NATIVE_INTEROP_PRIVATE_KEY_PASSPHRASE")
            .expect("fixture key passphrase");
        let private_key_pem =
            std::fs::read_to_string(private_key_path).expect("read fixture private key");

        let algorithm_policy = modern_algorithm_overrides();
        assert_eq!(
            algorithm_policy.kex_algorithms.as_deref(),
            Some(
                &MODERN_KEX_ALGORITHMS
                    .iter()
                    .map(|name| (*name).to_owned())
                    .collect::<Vec<_>>()[..]
            )
        );
        progress("pure-Rust algorithm policy");

        let owner = Uuid::new_v4();
        let store = Arc::new(EphemeralConnectionStore::new());
        let password_profile = store
            .create(
                owner,
                interoperability_profile(
                    host.clone(),
                    port,
                    username.clone(),
                    ProfileAuthenticationInput::Password { password },
                    "password-interoperability",
                ),
            )
            .expect("create password profile");
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        let admission = OperationAdmission::new();

        trust_fixture_host(&ssh, password_profile.id);
        progress("password trust and authentication");
        let password_result = execute_fixture_command(
            &ssh,
            &admission,
            password_profile.id,
            "printf native-password-ok",
        );
        assert_eq!(password_result.output, "native-password-ok\n");
        assert_eq!(password_result.exit_status, 0);
        assert_eq!(password_result.working_directory, "/workspace");
        assert!(!password_result.truncated);
        progress("password command");

        let initial_listing = ssh
            .list_directory(password_profile.id, "/workspace".to_owned())
            .expect("list fixture directory");
        assert!(!initial_listing.truncated);
        assert_eq!(initial_listing.entries.len(), 1);
        assert_eq!(initial_listing.entries[0].name, "hello.txt");
        assert!(!initial_listing.entries[0].is_directory);
        assert_eq!(
            initial_listing.entries[0].size,
            Some("Terminal233 native SFTP fixture\n".len() as u64)
        );
        progress("initial SFTP listing");

        let sftp = Arc::new(SftpTransferService::new(Arc::clone(&ssh)));
        {
            let permit = admission
                .try_acquire(password_profile.id)
                .expect("admit mkdir");
            sftp.create_directory(
                password_profile.id,
                "/workspace/new-directory".to_owned(),
                permit.close_signal(),
            )
            .expect("create remote directory");
        }
        progress("SFTP mkdir");
        {
            let permit = admission
                .try_acquire(password_profile.id)
                .expect("admit rename");
            sftp.rename_remote(
                password_profile.id,
                "/workspace/hello.txt".to_owned(),
                "/workspace/renamed.txt".to_owned(),
                false,
                permit.close_signal(),
            )
            .expect("rename remote file");
        }
        progress("SFTP rename");
        {
            let permit = admission
                .try_acquire(password_profile.id)
                .expect("admit file deletion");
            sftp.delete_remote(
                password_profile.id,
                "/workspace/renamed.txt".to_owned(),
                permit.close_signal(),
            )
            .expect("delete remote file");
        }
        progress("SFTP file delete");
        {
            let permit = admission
                .try_acquire(password_profile.id)
                .expect("admit directory deletion");
            sftp.delete_remote(
                password_profile.id,
                "/workspace/new-directory".to_owned(),
                permit.close_signal(),
            )
            .expect("delete remote directory");
        }
        progress("SFTP directory delete");
        let final_listing = ssh
            .list_directory(password_profile.id, "/workspace".to_owned())
            .expect("list mutated fixture directory");
        assert!(final_listing.entries.is_empty());
        progress("final SFTP listing");

        // Exercise the production transfer supervisor and workers without creating a WebView.
        // The payload spans multiple 64 KiB chunks, contains every byte value (including NUL),
        // and uses a Unicode remote name.
        let upload_bytes = (0..150_123_usize)
            .map(|index| ((index * 31 + 7) % 256) as u8)
            .collect::<Vec<_>>();
        let transfer_file_name = "猫咪-数据.bin";
        let transfer_remote_path = format!("/workspace/{transfer_file_name}");
        let (upload_sender, upload_receiver) = mpsc::channel();
        let upload_events = IpcChannel::new(move |body| {
            let event = body
                .deserialize::<Value>()
                .expect("deserialize upload IPC event");
            upload_sender.send(event).expect("capture upload IPC event");
            Ok::<(), String>(())
        });
        let upload_permit = admission
            .try_acquire(password_profile.id)
            .expect("admit upload transfer");
        let upload_started = sftp
            .start_upload_headless(
                password_profile.id,
                "/workspace".to_owned(),
                vec![SshSftpUploadInput {
                    name: transfer_file_name.to_owned(),
                    data_base64: STANDARD.encode(&upload_bytes),
                    overwrite: false,
                }],
                upload_events,
                upload_permit,
            )
            .expect("start production upload worker");
        assert_eq!(upload_started.total_files, 1);
        assert_eq!(upload_started.total_bytes, Some(upload_bytes.len() as u64));
        assert_eq!(upload_started.items[0].name, transfer_file_name);
        let upload_event_values = receive_transfer_events(&upload_receiver);
        assert_eq!(upload_event_values[0]["kind"], "started");
        assert_eq!(
            upload_event_values.last().expect("upload completion")["status"],
            "completed"
        );
        let upload_progress = upload_event_values
            .iter()
            .filter(|event| event["kind"] == "progress")
            .collect::<Vec<_>>();
        assert!(
            upload_progress.len() >= 3,
            "multi-chunk binary upload must report bounded progress"
        );
        let mut previous_uploaded = 0_u64;
        for event in &upload_progress {
            assert_eq!(event["jobId"], upload_started.job_id.to_string());
            assert_eq!(event["name"], transfer_file_name);
            let uploaded = event["fileBytesTransferred"]
                .as_u64()
                .expect("numeric upload progress");
            assert!(uploaded > previous_uploaded);
            assert!(uploaded <= upload_bytes.len() as u64);
            previous_uploaded = uploaded;
        }
        assert_eq!(previous_uploaded, upload_bytes.len() as u64);
        let upload_completed = upload_event_values.last().expect("upload completion");
        assert_eq!(upload_completed["completedFiles"], 1);
        assert_eq!(upload_completed["failedFiles"], 0);
        assert_eq!(upload_completed["cancelledFiles"], 0);
        assert_eq!(
            upload_completed["bytesTransferred"],
            upload_bytes.len() as u64
        );
        progress("production SFTP upload worker");

        let download_service = Arc::clone(&sftp);
        let (download_sender, download_receiver) = mpsc::channel();
        let download_events = IpcChannel::new(move |body| {
            let event = body
                .deserialize::<Value>()
                .expect("deserialize download IPC event");
            if event["kind"] == "chunk" {
                let job_id = event["jobId"]
                    .as_str()
                    .expect("download chunk job id")
                    .parse::<Uuid>()
                    .expect("valid download chunk job id");
                let sequence = event["sequence"].as_u64().expect("download chunk sequence");
                download_service
                    .acknowledge_download(job_id, sequence)
                    .expect("acknowledge production download chunk");
            }
            download_sender
                .send(event)
                .expect("capture download IPC event");
            Ok::<(), String>(())
        });
        let download_permit = admission
            .try_acquire(password_profile.id)
            .expect("admit download transfer");
        let download_started = sftp
            .start_download_headless(
                password_profile.id,
                vec![SshSftpDownloadInput {
                    remote_path: transfer_remote_path,
                    save_as: "下载-猫咪.bin".to_owned(),
                }],
                download_events,
                download_permit,
            )
            .expect("start production download worker");
        let download_event_values = receive_transfer_events(&download_receiver);
        assert_eq!(download_event_values[0]["kind"], "started");
        assert_eq!(
            download_event_values.last().expect("download completion")["status"],
            "completed"
        );
        let mut downloaded = Vec::new();
        let mut expected_sequence = 1_u64;
        let mut expected_offset = 0_u64;
        for event in download_event_values
            .iter()
            .filter(|event| event["kind"] == "chunk")
        {
            assert_eq!(event["jobId"], download_started.job_id.to_string());
            assert_eq!(event["name"], "下载-猫咪.bin");
            assert_eq!(event["sequence"], expected_sequence);
            assert_eq!(event["offset"], expected_offset);
            let chunk = STANDARD
                .decode(event["dataBase64"].as_str().expect("base64 download chunk"))
                .expect("decode download IPC chunk");
            assert!(!chunk.is_empty());
            assert!(chunk.len() <= 64 * 1024);
            downloaded.extend_from_slice(&chunk);
            expected_offset += chunk.len() as u64;
            expected_sequence += 1;
        }
        assert!(
            expected_sequence >= 4,
            "download must cross at least three acknowledged IPC chunks"
        );
        assert_eq!(downloaded, upload_bytes);
        let download_completed = download_event_values.last().expect("download completion");
        assert_eq!(download_completed["completedFiles"], 1);
        assert_eq!(download_completed["failedFiles"], 0);
        assert_eq!(download_completed["cancelledFiles"], 0);
        assert_eq!(
            download_completed["bytesTransferred"],
            downloaded.len() as u64
        );
        progress("production SFTP download worker");

        let private_key_profile = store
            .create(
                owner,
                interoperability_profile(
                    host,
                    port,
                    username,
                    ProfileAuthenticationInput::PrivateKey {
                        private_key_pem: private_key_pem.clone(),
                        passphrase: Some(private_key_passphrase.clone()),
                    },
                    "private-key-interoperability",
                ),
            )
            .expect("create private-key profile");
        trust_fixture_host(&ssh, private_key_profile.id);
        progress("private-key trust and authentication");
        let private_key_result = execute_fixture_command(
            &ssh,
            &admission,
            private_key_profile.id,
            "printf native-key-ok",
        );
        assert_eq!(private_key_result.output, "native-key-ok\n");
        assert_eq!(private_key_result.exit_status, 0);
        assert!(!private_key_result.truncated);
        progress("private-key command");

        let wrong_passphrase_profile = store
            .create(
                owner,
                interoperability_profile(
                    std::env::var("NEKOSSH_NATIVE_INTEROP_HOST").expect("fixture host"),
                    port,
                    std::env::var("NEKOSSH_NATIVE_INTEROP_USERNAME").expect("fixture username"),
                    ProfileAuthenticationInput::PrivateKey {
                        private_key_pem,
                        passphrase: Some(format!("{private_key_passphrase}-wrong")),
                    },
                    "wrong-private-key-passphrase-interoperability",
                ),
            )
            .expect("create wrong-passphrase profile");
        let confirmation_id = match ssh
            .prepare(wrong_passphrase_profile.id)
            .expect("prepare wrong-passphrase fixture host")
        {
            SshHostKeyStatus::ConfirmationRequired {
                confirmation_id, ..
            } => confirmation_id,
            unexpected => panic!("fixture host must require confirmation: {unexpected:?}"),
        };
        let authentication_started = Instant::now();
        assert!(matches!(
            ssh.trust_host_key(wrong_passphrase_profile.id, confirmation_id),
            Err(NekoError::SshAuthenticationFailed)
        ));
        assert!(
            authentication_started.elapsed() < Duration::from_secs(5),
            "wrong encrypted private-key passphrase must fail without an unbounded native prompt"
        );
        progress("wrong private-key passphrase rejected");
    }

    /// Opt-in acceptance against a real user-provided Linux SSH server. The fixed workspace is
    /// created below /tmp, only the exact files produced by this test may be removed, and the Drop
    /// guard repeats cleanup if an assertion or transfer worker fails.
    #[test]
    fn native_remote_ssh_interoperability() {
        if !remote_interop_enabled() {
            return;
        }

        let owner = Uuid::new_v4();
        let store = Arc::new(EphemeralConnectionStore::new());
        let profile = store
            .create(owner, remote_interop_profile())
            .expect("create remote SSH profile");
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        let admission = OperationAdmission::new();

        trust_fixture_host(&ssh, profile.id);
        let exec_result = remote_interop_command(
            &ssh,
            &admission,
            profile.id,
            "printf 'terminal233-remote-exec-ok\\n'",
        );
        assert_eq!(exec_result.output, "terminal233-remote-exec-ok\n");
        assert_eq!(exec_result.exit_status, 0);
        assert!(!exec_result.truncated);

        let tmp_listing = ssh
            .list_directory(profile.id, "/tmp".to_owned())
            .expect("list remote /tmp");
        assert!(!tmp_listing.truncated);

        let sftp = Arc::new(SftpTransferService::new(Arc::clone(&ssh)));
        let terminal = Arc::new(TerminalService::new(Arc::clone(&ssh)));
        let mut cleanup = RemoteInteropCleanup {
            ssh: Arc::clone(&ssh),
            service: Arc::clone(&sftp),
            terminal: Arc::clone(&terminal),
            admission: admission.clone(),
            profile_id: profile.id,
            armed: true,
        };

        let workspace_state = remote_interop_command(
            &ssh,
            &admission,
            profile.id,
            "if [ -d '/tmp/terminal233-remote-interop-v1' ]; then printf dir; elif [ ! -e '/tmp/terminal233-remote-interop-v1' ]; then printf missing; else printf other; fi",
        );
        match workspace_state.output.trim() {
            "missing" => {
                let permit = admission
                    .try_acquire(profile.id)
                    .expect("admit remote workspace creation");
                sftp.create_directory(
                    profile.id,
                    REMOTE_INTEROP_DIRECTORY.to_owned(),
                    permit.close_signal(),
                )
                .expect("create remote interop directory");
            }
            "dir" => {
                clean_remote_interop_directory(&ssh, &sftp, &admission, profile.id, false);
                assert!(
                    ssh.list_directory(profile.id, REMOTE_INTEROP_DIRECTORY.to_owned())
                        .expect("list cleaned remote interop directory")
                        .entries
                        .is_empty()
                );
            }
            "other" => panic!(
                "fixed remote interop path exists but is not a directory: {REMOTE_INTEROP_DIRECTORY}"
            ),
            unexpected => panic!("unexpected remote workspace state: {unexpected:?}"),
        }

        let upload_bytes = (0..131_097_usize)
            .map(|index| ((index * 31 + 7) % 256) as u8)
            .collect::<Vec<_>>();
        let (upload_sender, upload_receiver) = mpsc::channel();
        let upload_events = IpcChannel::new(move |body| {
            let event = body
                .deserialize::<Value>()
                .expect("deserialize remote upload event");
            upload_sender
                .send(event)
                .map_err(|_| "remote upload receiver dropped")
        });
        let upload_permit = admission
            .try_acquire(profile.id)
            .expect("admit remote upload");
        let upload_started = sftp
            .start_upload_headless(
                profile.id,
                REMOTE_INTEROP_DIRECTORY.to_owned(),
                vec![SshSftpUploadInput {
                    name: REMOTE_INTEROP_FILE.to_owned(),
                    data_base64: STANDARD.encode(&upload_bytes),
                    overwrite: false,
                }],
                upload_events,
                upload_permit,
            )
            .expect("start remote upload");
        let upload_events = receive_transfer_events(&upload_receiver);
        assert_eq!(upload_started.total_files, 1);
        assert_eq!(
            upload_events.last().expect("remote upload completion")["status"],
            "completed"
        );
        assert_eq!(
            upload_events.last().expect("remote upload completion")["bytesTransferred"],
            upload_bytes.len() as u64
        );

        let uploaded_listing = ssh
            .list_directory(profile.id, REMOTE_INTEROP_DIRECTORY.to_owned())
            .expect("list uploaded remote file");
        assert_eq!(uploaded_listing.entries.len(), 1);
        assert_eq!(uploaded_listing.entries[0].name, REMOTE_INTEROP_FILE);
        assert_eq!(
            uploaded_listing.entries[0].size,
            Some(upload_bytes.len() as u64)
        );

        let download_service = Arc::clone(&sftp);
        let (download_sender, download_receiver) = mpsc::channel();
        let download_events = IpcChannel::new(move |body| {
            let event = body
                .deserialize::<Value>()
                .expect("deserialize remote download event");
            if event["kind"] == "chunk" {
                let job_id = event["jobId"]
                    .as_str()
                    .expect("remote download job id")
                    .parse::<Uuid>()
                    .expect("valid remote download job id");
                let sequence = event["sequence"]
                    .as_u64()
                    .expect("remote download sequence");
                download_service
                    .acknowledge_download(job_id, sequence)
                    .expect("acknowledge remote download chunk");
            }
            download_sender
                .send(event)
                .map_err(|_| "remote download receiver dropped")
        });
        let download_permit = admission
            .try_acquire(profile.id)
            .expect("admit remote download");
        let download_started = sftp
            .start_download_headless(
                profile.id,
                vec![SshSftpDownloadInput {
                    remote_path: format!("{REMOTE_INTEROP_DIRECTORY}/{REMOTE_INTEROP_FILE}"),
                    save_as: REMOTE_INTEROP_FILE.to_owned(),
                }],
                download_events,
                download_permit,
            )
            .expect("start remote download");
        let download_events = receive_transfer_events(&download_receiver);
        assert_eq!(download_started.total_files, 1);
        let mut downloaded = Vec::new();
        for event in download_events
            .iter()
            .filter(|event| event["kind"] == "chunk")
        {
            let chunk = STANDARD
                .decode(event["dataBase64"].as_str().expect("remote download bytes"))
                .expect("decode remote download bytes");
            downloaded.extend_from_slice(&chunk);
        }
        assert_eq!(downloaded, upload_bytes);
        assert_eq!(
            download_events.last().expect("remote download completion")["status"],
            "completed"
        );

        let rename_permit = admission
            .try_acquire(profile.id)
            .expect("admit remote rename");
        sftp.rename_remote(
            profile.id,
            format!("{REMOTE_INTEROP_DIRECTORY}/{REMOTE_INTEROP_FILE}"),
            format!("{REMOTE_INTEROP_DIRECTORY}/{REMOTE_INTEROP_RENAMED_FILE}"),
            false,
            rename_permit.close_signal(),
        )
        .expect("rename remote test file");
        remote_interop_delete(
            &sftp,
            &admission,
            profile.id,
            &format!("{REMOTE_INTEROP_DIRECTORY}/{REMOTE_INTEROP_RENAMED_FILE}"),
        );
        remote_interop_delete(&sftp, &admission, profile.id, REMOTE_INTEROP_DIRECTORY);
        // The remote SFTP channel close is asynchronous on some OpenSSH/PAM stacks. Give the
        // server a bounded drain window before opening a fresh PTY transport.
        std::thread::sleep(Duration::from_secs(2));

        let (terminal_sender, terminal_receiver) = mpsc::channel();
        let terminal_events = {
            let event_service = Arc::clone(&terminal);
            IpcChannel::new(move |body| {
                let event = body
                    .deserialize::<Value>()
                    .expect("deserialize remote terminal event");
                if event["kind"] == "output" {
                    let session_id = event["sessionId"]
                        .as_str()
                        .expect("remote terminal session id")
                        .parse::<Uuid>()
                        .expect("valid remote terminal session id");
                    let sequence = event["sequence"]
                        .as_u64()
                        .expect("remote terminal output sequence");
                    event_service
                        .acknowledge_output(session_id, sequence)
                        .expect("acknowledge remote terminal output");
                }
                terminal_sender
                    .send(event)
                    .map_err(|_| "remote terminal receiver dropped")
            })
        };
        let terminal_permit = admission
            .try_acquire(profile.id)
            .expect("admit remote terminal");
        let started = terminal
            .start(
                profile.id,
                Some(100),
                Some(30),
                terminal_events,
                terminal_permit.close_signal(),
            )
            .expect("start remote terminal");
        terminal
            .resize(started.session_id, 132, 40)
            .expect("resize remote terminal");
        terminal
            .write(
                started.session_id,
                "printf '\\nterminal233-remote-pty-ok\\n'\n".to_owned(),
            )
            .expect("write remote terminal input");
        let deadline = Instant::now() + Duration::from_secs(30);
        let mut terminal_output = Vec::new();
        loop {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .expect("remote terminal must return a marker before the deadline");
            let event = terminal_receiver
                .recv_timeout(remaining)
                .expect("receive remote terminal event");
            match event["kind"].as_str() {
                Some("output") => {
                    let bytes = STANDARD
                        .decode(event["dataBase64"].as_str().expect("remote terminal bytes"))
                        .expect("decode remote terminal bytes");
                    terminal_output.extend_from_slice(&bytes);
                    if terminal_output
                        .windows(b"terminal233-remote-pty-ok".len())
                        .any(|window| window == b"terminal233-remote-pty-ok")
                    {
                        break;
                    }
                }
                Some("error") => panic!("remote terminal returned an error: {event}"),
                Some("closed") => panic!("remote terminal closed before its marker"),
                _ => panic!("unexpected remote terminal event: {event}"),
            }
        }
        terminal
            .close(started.session_id)
            .expect("close remote terminal");
        loop {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .expect("remote terminal must close before the deadline");
            let event = terminal_receiver
                .recv_timeout(remaining)
                .expect("receive remote terminal close event");
            match event["kind"].as_str() {
                Some("output") => {}
                Some("closed") => break,
                Some("error") => panic!("remote terminal returned an error: {event}"),
                _ => panic!("unexpected remote terminal close event: {event}"),
            }
        }

        cleanup.run();
    }

    #[test]
    fn modern_algorithm_policy_excludes_sha1_ssh_rsa_cbc_and_md5() {
        for policy in [
            MODERN_KEX_ALGORITHMS,
            MODERN_HOST_KEY_ALGORITHMS,
            MODERN_CIPHER_ALGORITHMS,
            MODERN_MAC_ALGORITHMS,
        ] {
            assert!(!policy.is_empty());
            for algorithm in policy {
                let normalized = algorithm.to_ascii_lowercase();
                assert!(!normalized.contains("sha1"), "{algorithm}");
                assert!(!normalized.contains("md5"), "{algorithm}");
                assert!(!normalized.contains("cbc"), "{algorithm}");
                assert_ne!(*algorithm, "ssh-rsa");
            }
        }
        assert!(MODERN_KEX_ALGORITHMS.contains(&"curve25519-sha256"));
        assert!(MODERN_HOST_KEY_ALGORITHMS.contains(&"rsa-sha2-512"));
        assert!(MODERN_CIPHER_ALGORITHMS.contains(&"aes256-gcm@openssh.com"));
        assert!(MODERN_MAC_ALGORITHMS.contains(&"hmac-sha2-512-etm@openssh.com"));
        assert!(matches!(
            validated_host_key_algorithm("ssh-rsa"),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert_eq!(
            validated_host_key_algorithm("rsa-sha2-512").expect("modern RSA signature"),
            "rsa-sha2-512"
        );
        assert!(matches!(
            validated_host_key_algorithm("ssh-dss"),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
    }

    #[test]
    fn rsa_host_keys_must_be_at_least_2048_bits() {
        fn append_field(blob: &mut Vec<u8>, value: &[u8]) {
            blob.extend_from_slice(&(value.len() as u32).to_be_bytes());
            blob.extend_from_slice(value);
        }

        fn rsa_blob(exponent: &[u8], modulus: &[u8]) -> Vec<u8> {
            let mut blob = Vec::new();
            append_field(&mut blob, b"ssh-rsa");
            append_field(&mut blob, exponent);
            append_field(&mut blob, modulus);
            blob
        }

        let mut strong_modulus = vec![0_u8; 257];
        strong_modulus[1] = 0x80;
        assert!(
            validate_host_key_strength(
                "rsa-sha2-512",
                &rsa_blob(&[0x01, 0x00, 0x01], &strong_modulus)
            )
            .is_ok()
        );

        let mut weak_modulus = vec![0_u8; 256];
        weak_modulus[1] = 0x40;
        assert!(matches!(
            validate_host_key_strength(
                "rsa-sha2-256",
                &rsa_blob(&[0x01, 0x00, 0x01], &weak_modulus)
            ),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert!(matches!(
            validate_host_key_strength("rsa-sha2-512", &rsa_blob(&[0x03], &strong_modulus)),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert!(matches!(
            validate_host_key_strength("rsa-sha2-512", b"malformed"),
            Err(NekoError::SshHostKeyUnavailable)
        ));
        assert!(validate_host_key_strength("ssh-ed25519", b"").is_err());
        assert!(validate_host_key_strength("ssh-ed25519", b"non-empty").is_ok());
    }

    #[test]
    fn puressh_has_every_algorithm_in_the_modern_policy() {
        for cipher in MODERN_CIPHER_ALGORITHMS {
            assert!(puressh::cipher::by_name(cipher).is_some(), "{cipher}");
        }
        for mac in MODERN_MAC_ALGORITHMS {
            assert!(puressh::mac::by_name(mac).is_some(), "{mac}");
        }
        for host_key in MODERN_HOST_KEY_ALGORITHMS {
            assert!(
                puressh::hostkey::HOST_KEY_VERIFY_NAMES.contains(host_key),
                "{host_key}"
            );
        }
        assert!(!puressh::hostkey::allow_rsa_sha1());
    }

    #[test]
    fn negotiated_mac_policy_distinguishes_aead_from_ctr() {
        assert!(negotiated_transport_algorithms_are_allowed(
            "aes256-gcm@openssh.com",
            None
        ));
        assert!(negotiated_transport_algorithms_are_allowed(
            "aes256-ctr",
            Some("hmac-sha2-256-etm@openssh.com")
        ));
        assert!(!negotiated_transport_algorithms_are_allowed(
            "aes256-ctr",
            Some("INTEGRATED-AES-GCM")
        ));
        assert!(!negotiated_transport_algorithms_are_allowed(
            "aes256-ctr",
            None
        ));
        assert!(!negotiated_transport_algorithms_are_allowed(
            "3des-cbc",
            Some("hmac-sha2-256")
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
