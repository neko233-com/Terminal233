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
use ssh2::{ErrorCode, ExtendedData, HostKeyType, MethodType, Session};
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
// libssh2 uses LIBSSH2_ERROR_FILE when a raw SFTP directory handle reaches EOF.
const LIBSSH2_ERROR_FILE: i32 = -16;
const MODERN_KEX_ALGORITHMS: &[&str] = &[
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
    "aes256-gcm@openssh.com",
    "aes128-gcm@openssh.com",
    "aes256-ctr",
    "aes192-ctr",
    "aes128-ctr",
];
const MODERN_AEAD_CIPHER_ALGORITHMS: &[&str] =
    &["aes256-gcm@openssh.com", "aes128-gcm@openssh.com"];
const MODERN_MAC_ALGORITHMS: &[&str] = &[
    "hmac-sha2-512-etm@openssh.com",
    "hmac-sha2-256-etm@openssh.com",
    "hmac-sha2-512",
    "hmac-sha2-256",
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
        let session = connect_and_handshake(&profile)?;
        let presented = presented_host_key(&session)?;

        match self.profiles.verify_host_key(
            profile_id,
            &profile.host,
            profile.port,
            &presented.key_algorithm,
            &presented.public_key,
        )? {
            HostKeyVerification::Trusted { fingerprint } => {
                authenticate(&session, &profile)?;
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
                configure_modern_algorithm_policy(&session)?;
                session.set_timeout(SSH_OPERATION_TIMEOUT_MS);
                session.set_tcp_stream(stream);
                session
                    .handshake()
                    .map_err(|_| NekoError::SshHandshakeFailed)?;
                validate_negotiated_algorithm_policy(&session)?;
                return Ok(session);
            }
            Err(_) => continue,
        }
    }
    Err(NekoError::SshConnectionFailed)
}

fn configure_modern_algorithm_policy(session: &Session) -> Result<()> {
    for (method_type, policy) in algorithm_policies() {
        let supported = session
            .supported_algs(method_type)
            .map_err(|_| NekoError::SshHandshakeFailed)?;
        let preferences = supported_policy_preferences(policy, &supported)
            .ok_or(NekoError::SshHandshakeFailed)?;
        session
            .method_pref(method_type, &preferences)
            .map_err(|_| NekoError::SshHandshakeFailed)?;
    }
    Ok(())
}

fn validate_negotiated_algorithm_policy(session: &Session) -> Result<()> {
    validate_negotiated_method(session, MethodType::Kex, MODERN_KEX_ALGORITHMS)?;
    validate_negotiated_method(session, MethodType::HostKey, MODERN_HOST_KEY_ALGORITHMS)?;
    validate_negotiated_transport_direction(session, MethodType::CryptCs, MethodType::MacCs)?;
    validate_negotiated_transport_direction(session, MethodType::CryptSc, MethodType::MacSc)?;
    Ok(())
}

fn validate_negotiated_method(
    session: &Session,
    method_type: MethodType,
    policy: &[&str],
) -> Result<()> {
    let negotiated = session
        .methods(method_type)
        .ok_or(NekoError::SshHandshakeFailed)?;
    if policy.contains(&negotiated) {
        Ok(())
    } else {
        Err(NekoError::SshHandshakeFailed)
    }
}

fn validate_negotiated_transport_direction(
    session: &Session,
    cipher_type: MethodType,
    mac_type: MethodType,
) -> Result<()> {
    let cipher = session
        .methods(cipher_type)
        .ok_or(NekoError::SshHandshakeFailed)?;
    // AES-GCM is an AEAD construction, so libssh2 replaces the separately negotiated MAC with
    // an internal display-only marker. Do not couple policy enforcement to that private marker.
    let mac = if MODERN_AEAD_CIPHER_ALGORITHMS.contains(&cipher) {
        None
    } else {
        Some(
            session
                .methods(mac_type)
                .ok_or(NekoError::SshHandshakeFailed)?,
        )
    };
    if negotiated_transport_algorithms_are_allowed(cipher, mac) {
        Ok(())
    } else {
        Err(NekoError::SshHandshakeFailed)
    }
}

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

fn algorithm_policies() -> [(MethodType, &'static [&'static str]); 6] {
    [
        (MethodType::Kex, MODERN_KEX_ALGORITHMS),
        (MethodType::HostKey, MODERN_HOST_KEY_ALGORITHMS),
        (MethodType::CryptCs, MODERN_CIPHER_ALGORITHMS),
        (MethodType::CryptSc, MODERN_CIPHER_ALGORITHMS),
        (MethodType::MacCs, MODERN_MAC_ALGORITHMS),
        (MethodType::MacSc, MODERN_MAC_ALGORITHMS),
    ]
}

fn supported_policy_preferences(policy: &[&str], supported: &[&str]) -> Option<String> {
    let preferences = policy
        .iter()
        .copied()
        .filter(|candidate| supported.contains(candidate))
        .collect::<Vec<_>>();
    (!preferences.is_empty()).then(|| preferences.join(","))
}

fn presented_host_key(session: &Session) -> Result<PresentedHostKey> {
    let (public_key, key_type) = session.host_key().ok_or(NekoError::SshHostKeyUnavailable)?;
    if public_key.is_empty() {
        return Err(NekoError::SshHostKeyUnavailable);
    }
    // DSA (`ssh-dss`) is obsolete and must never enter the trust flow, even when the bundled
    // libssh2 build can negotiate it.  Keep the remaining host-key types explicit so every key
    // that can be trusted is still fingerprinted and verified by the profile store.
    let negotiated = session
        .methods(MethodType::HostKey)
        .ok_or(NekoError::SshHostKeyUnavailable)?;
    let key_algorithm = validated_host_key_algorithm(key_type, negotiated)?;
    validate_host_key_strength(key_type, public_key)?;
    Ok(PresentedHostKey {
        key_algorithm: key_algorithm.to_owned(),
        public_key: public_key.to_vec(),
    })
}

fn validated_host_key_algorithm(key_type: HostKeyType, negotiated_algorithm: &str) -> Result<&str> {
    if !MODERN_HOST_KEY_ALGORITHMS.contains(&negotiated_algorithm) {
        return Err(NekoError::SshHostKeyAlgorithmRejected);
    }
    let matches_key_type = match key_type {
        HostKeyType::Rsa => matches!(negotiated_algorithm, "rsa-sha2-512" | "rsa-sha2-256"),
        HostKeyType::Ecdsa256 => negotiated_algorithm == "ecdsa-sha2-nistp256",
        HostKeyType::Ecdsa384 => negotiated_algorithm == "ecdsa-sha2-nistp384",
        HostKeyType::Ecdsa521 => negotiated_algorithm == "ecdsa-sha2-nistp521",
        HostKeyType::Ed25519 => negotiated_algorithm == "ssh-ed25519",
        HostKeyType::Dss => return Err(NekoError::SshHostKeyAlgorithmRejected),
        HostKeyType::Unknown => return Err(NekoError::SshHostKeyUnavailable),
    };
    if matches_key_type {
        Ok(negotiated_algorithm)
    } else {
        Err(NekoError::SshHostKeyUnavailable)
    }
}

fn validate_host_key_strength(key_type: HostKeyType, public_key: &[u8]) -> Result<()> {
    if !matches!(key_type, HostKeyType::Rsa) {
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

fn authenticate(session: &Session, profile: &SshProfile) -> Result<()> {
    match &profile.authentication {
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
    use crate::{
        domain::{ProfileAuthenticationInput, SshProfileInput, TerminalPreferences},
        ephemeral::EphemeralConnectionStore,
        operation_admission::OperationAdmission,
        sftp_transfer::{SftpTransferService, SshSftpDownloadInput, SshSftpUploadInput},
    };
    use base64::{Engine as _, engine::general_purpose::STANDARD};
    use serde_json::Value;
    use std::sync::mpsc::{self, Receiver};
    use tauri::ipc::Channel as IpcChannel;

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
    /// loopback SSH/SFTP fixture server; this side remains the production in-process libssh2
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

        let probe_stream =
            TcpStream::connect((host.as_str(), port)).expect("connect to loopback SSH fixture");
        let mut probe_session = Session::new().expect("create loopback libssh2 session");
        if std::env::var("NEKOSSH_INTEROP_DEBUG").as_deref() == Ok("1") {
            for (index, (method_type, policy)) in algorithm_policies().into_iter().enumerate() {
                eprintln!(
                    "algorithm-policy-{index}: policy={policy:?} supported={:?}",
                    probe_session
                        .supported_algs(method_type)
                        .expect("read supported algorithms")
                );
            }
        }
        configure_modern_algorithm_policy(&probe_session)
            .expect("configure loopback modern algorithms");
        probe_session.set_timeout(SSH_OPERATION_TIMEOUT_MS);
        probe_session.set_tcp_stream(probe_stream);
        probe_session
            .handshake()
            .expect("complete loopback SSH handshake");
        validate_negotiated_algorithm_policy(&probe_session)
            .expect("validate loopback negotiated algorithms");
        drop(probe_session);
        progress("algorithm handshake");

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
            Some("NekoSSH native SFTP fixture\n".len() as u64)
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
            Ok(())
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
            Ok(())
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
            validated_host_key_algorithm(HostKeyType::Rsa, "ssh-rsa"),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert_eq!(
            validated_host_key_algorithm(HostKeyType::Rsa, "rsa-sha2-512")
                .expect("modern RSA signature"),
            "rsa-sha2-512"
        );
        assert!(matches!(
            validated_host_key_algorithm(HostKeyType::Dss, "ssh-ed25519"),
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
                HostKeyType::Rsa,
                &rsa_blob(&[0x01, 0x00, 0x01], &strong_modulus)
            )
            .is_ok()
        );

        let mut weak_modulus = vec![0_u8; 256];
        weak_modulus[1] = 0x40;
        assert!(matches!(
            validate_host_key_strength(
                HostKeyType::Rsa,
                &rsa_blob(&[0x01, 0x00, 0x01], &weak_modulus)
            ),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert!(matches!(
            validate_host_key_strength(HostKeyType::Rsa, &rsa_blob(&[0x03], &strong_modulus)),
            Err(NekoError::SshHostKeyAlgorithmRejected)
        ));
        assert!(matches!(
            validate_host_key_strength(HostKeyType::Rsa, b"malformed"),
            Err(NekoError::SshHostKeyUnavailable)
        ));
        assert!(validate_host_key_strength(HostKeyType::Ed25519, b"").is_ok());
    }

    #[test]
    fn bundled_libssh2_has_at_least_one_supported_algorithm_per_modern_policy() {
        let session = Session::new().expect("libssh2 session");
        configure_modern_algorithm_policy(&session).expect("modern policy is supported");
        assert_eq!(
            supported_policy_preferences(
                &["preferred", "fallback"],
                &["unrelated", "fallback", "preferred"]
            )
            .as_deref(),
            Some("preferred,fallback")
        );
        assert!(supported_policy_preferences(&["modern"], &["legacy"]).is_none());
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
