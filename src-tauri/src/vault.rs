//! Local encrypted-vault persistence.
//!
//! The vault uses a random 256-bit data-encryption key (DEK).  A password-derived Argon2id key
//! encryption key (KEK) only wraps that DEK.  Profile/private-key/sync-secret data is therefore
//! never written as plaintext and changing a password only re-wraps the DEK.

use std::{
    collections::BTreeMap,
    fs::{self, File, OpenOptions},
    io::Write,
    path::{Path, PathBuf},
    sync::Mutex,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    crypto::{
        Argon2idConfig, DEK_BYTES, EncryptedPayload, VAULT_FORMAT, aad, decrypt, derive_kek,
        encrypt, fresh_kdf_config, random_dek, unwrap_dek, validate_password, wrap_dek,
    },
    domain::{
        HostKeyVerification, KnownHostRecord, KnownHosts, ProfileSummary, SshProfile,
        SshProfileInput, SshProfilePatch, SshSessionPlan, SyncConfiguration,
        SyncConfigurationInput, SyncConfigurationSummary, SyncKind,
    },
    error::{Result, VaultError},
    sync::{
        EncryptedSyncBundle, SyncTransferPlan, SyncTransferReceipt, adapter_for,
        download_git_bundle, download_webdav_bundle, upload_git_bundle, upload_webdav_bundle,
        validate_webdav_etag,
    },
};

pub const VAULT_FILENAME: &str = "neko233-v1.vault.json";
pub const SSH_KNOWN_HOSTS_FILENAME: &str = "neko233-known_hosts";
const VAULT_DATA_SCHEMA: u32 = 1;

/// The only persisted top-level structure.  Every user-provided value is inside
/// `encrypted_payload`; the remaining metadata is required to derive and authenticate the key.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct VaultEnvelope {
    pub format: String,
    pub created_at_unix_ms: u64,
    pub updated_at_unix_ms: u64,
    pub kdf: Argon2idConfig,
    pub wrapped_dek: EncryptedPayload,
    pub encrypted_payload: EncryptedPayload,
}

impl VaultEnvelope {
    fn create_with_password(
        password: &str,
        data: &VaultData,
        now_unix_ms: u64,
    ) -> Result<(Self, Zeroizing<[u8; DEK_BYTES]>)> {
        let dek = random_dek()?;
        let kdf = fresh_kdf_config()?;
        let kek = derive_kek(password, &kdf)?;
        let wrapped_dek = wrap_dek(&dek, &kek)?;
        let encrypted_payload = encrypt_data(&dek, data)?;
        Ok((
            Self {
                format: VAULT_FORMAT.to_owned(),
                created_at_unix_ms: now_unix_ms,
                updated_at_unix_ms: now_unix_ms,
                kdf,
                wrapped_dek,
                encrypted_payload,
            },
            dek,
        ))
    }

    pub fn validate_structure(&self) -> Result<()> {
        if self.format != VAULT_FORMAT {
            return Err(VaultError::UnsupportedFormat);
        }
        self.kdf.validate()?;
        self.wrapped_dek.validate_encoding()?;
        self.encrypted_payload.validate_encoding()?;
        Ok(())
    }

    fn unwrap_with_password(&self, password: &str) -> Result<Zeroizing<[u8; DEK_BYTES]>> {
        self.validate_structure()?;
        let kek = derive_kek(password, &self.kdf)?;
        unwrap_dek(&self.wrapped_dek, &kek)
    }

    fn rewrap_password(
        &mut self,
        dek: &[u8; DEK_BYTES],
        new_password: &str,
        now_unix_ms: u64,
    ) -> Result<()> {
        let new_kdf = fresh_kdf_config()?;
        let new_kek = derive_kek(new_password, &new_kdf)?;
        self.wrapped_dek = wrap_dek(dek, &new_kek)?;
        self.kdf = new_kdf;
        self.updated_at_unix_ms = now_unix_ms;
        Ok(())
    }

    #[cfg(test)]
    pub(crate) fn test_fixture() -> Self {
        let data = VaultData::default();
        let password = "test fixture vault password";
        let (envelope, _) =
            Self::create_with_password(password, &data, 1).expect("test fixture envelope");
        envelope
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct VaultData {
    schema_version: u32,
    profiles: BTreeMap<Uuid, SshProfile>,
    known_hosts: KnownHosts,
    sync_configuration: Option<SyncConfiguration>,
    #[serde(default)]
    webdav_sync_state: Option<WebDavSyncState>,
}

impl Default for VaultData {
    fn default() -> Self {
        Self {
            schema_version: VAULT_DATA_SCHEMA,
            profiles: BTreeMap::new(),
            known_hosts: KnownHosts::default(),
            sync_configuration: None,
            webdav_sync_state: None,
        }
    }
}

impl VaultData {
    fn validate(&self) -> Result<()> {
        if self.schema_version != VAULT_DATA_SCHEMA {
            return Err(VaultError::UnsupportedFormat);
        }
        for (id, profile) in &self.profiles {
            if id != &profile.id {
                return Err(VaultError::Malformed);
            }
            profile.validate()?;
        }
        if let Some(configuration) = &self.sync_configuration {
            configuration.validate()?;
        }
        if let Some(state) = &self.webdav_sync_state {
            let configuration = self
                .sync_configuration
                .as_ref()
                .ok_or(VaultError::Malformed)?;
            state
                .validate_for(configuration)
                .map_err(|_| VaultError::Malformed)?;
        }
        Ok(())
    }
}

/// The remote version observed after a successful conditional WebDAV upload.  It is encrypted
/// with the rest of the vault and binds the ETag to one exact WebDAV artifact location.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
struct WebDavSyncState {
    endpoint: String,
    remote_path: String,
    etag: String,
}

impl WebDavSyncState {
    fn from_configuration(configuration: &SyncConfiguration, etag: String) -> Result<Self> {
        let SyncConfiguration::WebDav {
            endpoint,
            remote_path,
            ..
        } = configuration
        else {
            return Err(VaultError::SyncNotWebDav);
        };
        Ok(Self {
            endpoint: endpoint.clone(),
            remote_path: remote_path.clone(),
            etag: validate_webdav_etag(&etag)?,
        })
    }

    fn validate_for(&self, configuration: &SyncConfiguration) -> Result<()> {
        let expected = Self::from_configuration(configuration, self.etag.clone())?;
        if self.endpoint == expected.endpoint && self.remote_path == expected.remote_path {
            Ok(())
        } else {
            Err(VaultError::Malformed)
        }
    }

    fn matches(&self, configuration: &SyncConfiguration) -> bool {
        match configuration {
            SyncConfiguration::WebDav {
                endpoint,
                remote_path,
                ..
            } => {
                self.endpoint.as_str() == endpoint.as_str()
                    && self.remote_path.as_str() == remote_path.as_str()
            }
            SyncConfiguration::Git { .. } => false,
        }
    }
}

struct UnlockedVault {
    envelope: VaultEnvelope,
    data: VaultData,
    dek: Zeroizing<[u8; DEK_BYTES]>,
}

#[derive(Default)]
struct VaultRuntime {
    unlocked: Option<UnlockedVault>,
}

/// Thread-safe service that owns the only in-memory copy of an unlocked DEK.
pub struct VaultService {
    path: PathBuf,
    runtime: Mutex<VaultRuntime>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VaultStatus {
    pub exists: bool,
    pub unlocked: bool,
    pub format: Option<String>,
    pub vault_path_hint: String,
}

/// Safe preview information returned only after the supplied backup password has authenticated
/// both layers of the encrypted envelope.  It intentionally has no host, username, sync URL,
/// or credential fields.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VerifiedSyncBundle {
    pub format: String,
    pub exported_at_unix_ms: u64,
    pub encrypted_bundle_sha256: String,
    pub bytes: usize,
    pub profile_count: usize,
    pub sync_kind: Option<SyncKind>,
}

/// Result of an import or an explicit rollback.  The path hint is only a filename, never the
/// actual application-data directory, so it is safe to render in the webview.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct VaultImportReceipt {
    pub status: VaultStatus,
    pub encrypted_bundle_sha256: String,
    pub bytes: usize,
    pub backup_created: bool,
    pub rollback_available: bool,
    pub rollback_path_hint: Option<String>,
    pub restored_from_backup: bool,
}

impl VaultService {
    pub fn new(path: PathBuf) -> Self {
        Self {
            path,
            runtime: Mutex::new(VaultRuntime::default()),
        }
    }

    pub fn path(&self) -> &Path {
        &self.path
    }

    pub fn status(&self) -> Result<VaultStatus> {
        let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let exists = match fs::metadata(&self.path) {
            Ok(metadata) => metadata.is_file(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => false,
            Err(error) => return Err(VaultError::Storage(error)),
        };
        let format = guard
            .unlocked
            .as_ref()
            .map(|unlocked| unlocked.envelope.format.clone());
        Ok(VaultStatus {
            exists,
            unlocked: guard.unlocked.is_some(),
            format,
            // Never return the actual profile/vault location to remote web content.
            vault_path_hint: VAULT_FILENAME.to_owned(),
        })
    }

    /// Creates and unlocks a new local vault.  No default password/key exists.
    pub fn bootstrap(&self, password: &str) -> Result<VaultStatus> {
        validate_password(password)?;
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        if self.vault_exists()? {
            return Err(VaultError::AlreadyExists);
        }

        let now = now_unix_ms();
        let data = VaultData::default();
        let (envelope, dek) = VaultEnvelope::create_with_password(password, &data, now)?;
        write_envelope_atomic(&self.path, &envelope)?;
        guard.unlocked = Some(UnlockedVault {
            envelope,
            data,
            dek,
        });
        Ok(VaultStatus {
            exists: true,
            unlocked: true,
            format: Some(VAULT_FORMAT.to_owned()),
            vault_path_hint: VAULT_FILENAME.to_owned(),
        })
    }

    pub fn unlock(&self, password: &str) -> Result<VaultStatus> {
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        if guard.unlocked.is_some() {
            return Err(VaultError::AlreadyUnlocked);
        }
        let envelope = read_envelope(&self.path)?;
        let dek = envelope.unwrap_with_password(password)?;
        let data = decrypt_data(&dek, &envelope.encrypted_payload)?;
        data.validate()?;
        guard.unlocked = Some(UnlockedVault {
            envelope,
            data,
            dek,
        });
        Ok(VaultStatus {
            exists: true,
            unlocked: true,
            format: Some(VAULT_FORMAT.to_owned()),
            vault_path_hint: VAULT_FILENAME.to_owned(),
        })
    }

    pub fn lock(&self) -> Result<VaultStatus> {
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        guard.unlocked = None;
        drop(guard);
        self.status()
    }

    /// Rotates the KEK wrapper but preserves the random DEK and ciphertext payload.
    pub fn change_password(&self, current_password: &str, new_password: &str) -> Result<()> {
        validate_password(new_password)?;
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let mut envelope = read_envelope(&self.path)?;
        let dek = envelope.unwrap_with_password(current_password)?;
        if let Some(unlocked) = guard.unlocked.as_ref()
            && (unlocked.envelope != envelope || unlocked.dek.as_ref() != dek.as_ref())
        {
            // Another process (or a stale sync import) changed the vault while this runtime was
            // unlocked.  Continuing would pair the in-memory DEK/data with a different envelope
            // and could make the next transaction unrecoverable.
            return Err(VaultError::VaultChangedOnDisk);
        }
        envelope.rewrap_password(&dek, new_password, now_unix_ms())?;
        write_envelope_atomic(&self.path, &envelope)?;

        if let Some(unlocked) = guard.unlocked.as_mut() {
            unlocked.envelope = envelope;
        }
        Ok(())
    }

    pub fn create_profile(&self, input: SshProfileInput) -> Result<ProfileSummary> {
        self.transaction(move |data| {
            let profile = SshProfile::from_input(input, now_unix_ms())?;
            let summary = profile.summary();
            data.profiles.insert(profile.id, profile);
            Ok(summary)
        })
    }

    pub fn list_profiles(&self) -> Result<Vec<ProfileSummary>> {
        self.read_unlocked(|data| {
            let mut profiles: Vec<_> = data.profiles.values().map(SshProfile::summary).collect();
            profiles.sort_by_key(|profile| profile.label.to_lowercase());
            Ok(profiles)
        })
    }

    pub fn get_profile(&self, profile_id: Uuid) -> Result<ProfileSummary> {
        self.read_unlocked(|data| {
            data.profiles
                .get(&profile_id)
                .map(SshProfile::summary)
                .ok_or(VaultError::ProfileNotFound)
        })
    }

    /// Returns a strict OpenSSH-compatible session plan, never vaulted credential material.
    pub fn ssh_session_plan(&self, profile_id: Uuid) -> Result<SshSessionPlan> {
        let known_hosts_path = self.managed_known_hosts_path()?;
        self.read_unlocked(|data| {
            data.profiles
                .get(&profile_id)
                .ok_or(VaultError::ProfileNotFound)?
                .session_plan(&known_hosts_path)
        })
    }

    /// Starts a visible platform terminal for profiles that have a safe external OpenSSH
    /// invocation.  On Windows the returned PID is the Windows Terminal launcher PID.
    /// Vaulted PEM keys intentionally return `ExternalSshUnavailable`; a PTY-backed SSH adapter
    /// must consume those keys from protected process memory instead of writing a temp key file.
    pub fn launch_external_ssh(&self, profile_id: Uuid) -> Result<u32> {
        let known_hosts_path = self.managed_known_hosts_path()?;
        self.read_unlocked(|data| {
            let profile = data
                .profiles
                .get(&profile_id)
                .ok_or(VaultError::ProfileNotFound)?;
            profile.launch_external_ssh(&known_hosts_path)
        })
    }

    pub fn update_profile(
        &self,
        profile_id: Uuid,
        patch: SshProfilePatch,
    ) -> Result<ProfileSummary> {
        self.transaction(move |data| {
            let profile = data
                .profiles
                .get_mut(&profile_id)
                .ok_or(VaultError::ProfileNotFound)?;
            profile.apply_patch(patch, now_unix_ms())?;
            Ok(profile.summary())
        })
    }

    pub fn delete_profile(&self, profile_id: Uuid) -> Result<()> {
        self.transaction(move |data| {
            data.profiles
                .remove(&profile_id)
                .map(|_| ())
                .ok_or(VaultError::ProfileNotFound)
        })
    }

    pub fn configure_sync(
        &self,
        input: SyncConfigurationInput,
    ) -> Result<SyncConfigurationSummary> {
        self.transaction(move |data| {
            let configuration = SyncConfiguration::from_input(input)?;
            let summary = configuration.summary();
            data.sync_configuration = Some(configuration);
            // A configured endpoint may have changed even when its redacted summary looks the
            // same.  Require an explicit download/review rather than reusing an ETag from an old
            // WebDAV target.
            data.webdav_sync_state = None;
            Ok(summary)
        })
    }

    pub fn sync_configuration(&self) -> Result<Option<SyncConfigurationSummary>> {
        self.read_unlocked(|data| {
            Ok(data
                .sync_configuration
                .as_ref()
                .map(SyncConfiguration::summary))
        })
    }

    /// Produces a portable encrypted backup.  It can be called while locked because it never
    /// decrypts the envelope.
    pub fn export_encrypted_bundle(&self) -> Result<EncryptedSyncBundle> {
        let envelope = read_envelope(&self.path)?;
        Ok(EncryptedSyncBundle::from_envelope(&envelope, now_unix_ms()))
    }

    /// Authenticates an encrypted backup with its own password before exposing a safe preview.
    /// This decrypts only in local process memory; no local vault file is changed.
    pub fn verify_encrypted_bundle(
        &self,
        bundle: &EncryptedSyncBundle,
        password: &str,
    ) -> Result<VerifiedSyncBundle> {
        let data = validate_bundle_with_password(bundle, password)?;
        let bytes = checked_bundle_bytes(bundle)?;
        Ok(VerifiedSyncBundle {
            format: bundle.format.clone(),
            exported_at_unix_ms: bundle.exported_at_unix_ms,
            encrypted_bundle_sha256: format!("sha256:{:x}", Sha256::digest(&bytes)),
            bytes: bytes.len(),
            profile_count: data.profiles.len(),
            sync_kind: data
                .sync_configuration
                .as_ref()
                .map(SyncConfiguration::kind),
        })
    }

    /// Replaces the local envelope only after the supplied backup password has authenticated the
    /// encrypted DEK wrapper and payload.  The prior local envelope is atomically copied to a
    /// neighboring `.bak` file first, and the imported vault remains locked afterwards.
    pub fn import_encrypted_bundle(
        &self,
        bundle: EncryptedSyncBundle,
        password: &str,
    ) -> Result<VaultImportReceipt> {
        let preview = self.verify_encrypted_bundle(&bundle, password)?;
        let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        if guard.unlocked.is_some() {
            return Err(VaultError::VaultMustBeLockedForImport);
        }
        let backup_created = self.back_up_current_envelope()?;
        write_envelope_atomic(&self.path, &bundle.vault)?;
        Ok(VaultImportReceipt {
            status: locked_vault_status(),
            encrypted_bundle_sha256: preview.encrypted_bundle_sha256,
            bytes: preview.bytes,
            backup_created,
            rollback_available: backup_created,
            rollback_path_hint: backup_created.then(|| import_backup_path_hint(&self.path)),
            restored_from_backup: false,
        })
    }

    /// Restores the preserved pre-import vault after validating it with the supplied password.
    /// The backup remains in place, so a failed or interrupted restore never destroys the only
    /// rollback copy.  This deliberately requires the local vault to be locked as well.
    pub fn restore_import_backup(&self, password: &str) -> Result<VaultImportReceipt> {
        let backup_path = import_backup_path(&self.path);
        let envelope = read_envelope(&backup_path).map_err(|error| match error {
            VaultError::Missing => VaultError::VaultImportBackupMissing,
            other => other,
        })?;
        let bundle = EncryptedSyncBundle::from_envelope(&envelope, 0);
        let preview = self.verify_encrypted_bundle(&bundle, password)?;
        let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        if guard.unlocked.is_some() {
            return Err(VaultError::VaultMustBeLockedForImport);
        }
        write_envelope_atomic(&self.path, &envelope)?;
        Ok(VaultImportReceipt {
            status: locked_vault_status(),
            encrypted_bundle_sha256: preview.encrypted_bundle_sha256,
            bytes: preview.bytes,
            backup_created: false,
            rollback_available: true,
            rollback_path_hint: Some(import_backup_path_hint(&self.path)),
            restored_from_backup: true,
        })
    }

    pub fn sync_upload_plan(&self) -> Result<SyncTransferPlan> {
        let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
        let configuration = unlocked
            .data
            .sync_configuration
            .as_ref()
            .ok_or(VaultError::SyncNotConfigured)?;
        let bundle = EncryptedSyncBundle::from_envelope(&unlocked.envelope, now_unix_ms());
        adapter_for(configuration)?.upload_plan(&bundle)
    }

    /// Performs a real WebDAV upload after copying only the encrypted envelope and the encrypted
    /// configuration out of the short vault lock.  Network I/O never runs while the mutex is held.
    pub fn sync_upload_webdav(&self) -> Result<SyncTransferReceipt> {
        let (configuration, bundle, expected_remote_etag) = {
            let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
            let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
            let configuration = unlocked
                .data
                .sync_configuration
                .as_ref()
                .ok_or(VaultError::SyncNotConfigured)?
                .clone();
            let expected_remote_etag = unlocked
                .data
                .webdav_sync_state
                .as_ref()
                .filter(|state| state.matches(&configuration))
                .map(|state| state.etag.clone());
            (
                configuration,
                EncryptedSyncBundle::from_envelope(&unlocked.envelope, now_unix_ms()),
                expected_remote_etag,
            )
        };
        let upload =
            upload_webdav_bundle(&configuration, &bundle, expected_remote_etag.as_deref())?;
        self.record_webdav_sync_state(&configuration, upload.remote_etag)?;
        Ok(upload.receipt)
    }

    /// Downloads a remote encrypted envelope for an explicit review/import step.  This never
    /// replaces the local vault automatically, avoiding silent last-writer-wins data loss.
    pub fn sync_download_webdav_bundle(&self) -> Result<EncryptedSyncBundle> {
        let configuration = self.read_unlocked(|data| {
            data.sync_configuration
                .as_ref()
                .cloned()
                .ok_or(VaultError::SyncNotConfigured)
        })?;
        download_webdav_bundle(&configuration)
    }

    /// Performs a real Git HTTPS backup after copying only an encrypted envelope and the
    /// encrypted-in-vault configuration out of the short runtime lock.  The libgit2 transport
    /// retains the token only in its credential callback and never passes it through a process
    /// command line, URL, environment variable, or receipt.
    pub fn sync_upload_git(&self) -> Result<SyncTransferReceipt> {
        let (configuration, bundle) = {
            let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
            let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
            let configuration = unlocked
                .data
                .sync_configuration
                .as_ref()
                .ok_or(VaultError::SyncNotConfigured)?
                .clone();
            (
                configuration,
                EncryptedSyncBundle::from_envelope(&unlocked.envelope, now_unix_ms()),
            )
        };
        upload_git_bundle(&configuration, &bundle)
    }

    /// Downloads an encrypted Git vault artifact for the same explicit password-verify/import
    /// confirmation flow as other cloud backups.  It never replaces the local vault by itself.
    pub fn sync_download_git_bundle(&self) -> Result<EncryptedSyncBundle> {
        let configuration = self.read_unlocked(|data| {
            data.sync_configuration
                .as_ref()
                .cloned()
                .ok_or(VaultError::SyncNotConfigured)
        })?;
        download_git_bundle(&configuration)
    }

    /// Restores the configured WebDAV backup without passing its encrypted bytes through the
    /// webview.  The active vault must be unlocked only long enough to read its WebDAV
    /// configuration; the downloaded envelope is password/AEAD-verified first, then the service
    /// locks itself, preserves a `.bak` rollback copy, and atomically imports the backup.
    pub fn sync_webdav_restore_import(&self, password: &str) -> Result<VaultImportReceipt> {
        let (configuration, expected_envelope) = {
            let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
            let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
            let configuration = unlocked
                .data
                .sync_configuration
                .as_ref()
                .cloned()
                .ok_or(VaultError::SyncNotConfigured)?;
            (configuration, unlocked.envelope.clone())
        };
        let bundle = download_webdav_bundle(&configuration)?;
        self.import_downloaded_sync_bundle(bundle, password, &configuration, &expected_envelope)
    }

    /// Downloads, password/AEAD-verifies, snapshots, locks, and atomically imports a Git backup
    /// wholly in Rust.  The encrypted bundle never crosses Tauri IPC, and a configuration or
    /// envelope change while network I/O is in flight is treated as a conflict.
    pub fn sync_git_restore_import(&self, password: &str) -> Result<VaultImportReceipt> {
        let (configuration, expected_envelope) = {
            let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
            let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
            let configuration = unlocked
                .data
                .sync_configuration
                .as_ref()
                .cloned()
                .ok_or(VaultError::SyncNotConfigured)?;
            (configuration, unlocked.envelope.clone())
        };
        let bundle = download_git_bundle(&configuration)?;
        self.import_downloaded_sync_bundle(bundle, password, &configuration, &expected_envelope)
    }

    pub fn verify_host_key(
        &self,
        host: &str,
        port: u16,
        key_algorithm: &str,
        public_key: &[u8],
    ) -> Result<HostKeyVerification> {
        self.read_unlocked(|data| {
            Ok(data
                .known_hosts
                .verify(host, port, key_algorithm, public_key))
        })
    }

    pub fn trust_host_key(
        &self,
        host: String,
        port: u16,
        key_algorithm: String,
        public_key: &[u8],
    ) -> Result<()> {
        let public_key = public_key.to_vec();
        self.transaction(move |data| {
            let record = KnownHostRecord::from_presented(
                host,
                port,
                key_algorithm,
                &public_key,
                now_unix_ms(),
            )?;
            data.known_hosts.trust(record);
            Ok(())
        })
    }

    fn managed_known_hosts_path(&self) -> Result<PathBuf> {
        let parent = self.path.parent().ok_or_else(|| {
            VaultError::Storage(std::io::Error::other("vault path has no parent directory"))
        })?;
        fs::create_dir_all(parent).map_err(VaultError::Storage)?;
        let path = parent.join(SSH_KNOWN_HOSTS_FILENAME);
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(false)
            .open(&path)
            .map_err(VaultError::Storage)?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;

            fs::set_permissions(&path, fs::Permissions::from_mode(0o600))
                .map_err(VaultError::Storage)?;
        }
        Ok(path)
    }

    fn back_up_current_envelope(&self) -> Result<bool> {
        match fs::metadata(&self.path) {
            Ok(metadata) if metadata.is_file() => {
                // Refuse to overwrite an unreadable local vault.  This is deliberately stricter
                // than merely copying bytes: an import must never be the operation that erases a
                // potentially recoverable local artifact.
                let current = read_envelope(&self.path)?;
                write_envelope_atomic(&import_backup_path(&self.path), &current)?;
                Ok(true)
            }
            Ok(_) => Err(VaultError::Storage(std::io::Error::other(
                "vault path is not a regular file",
            ))),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
            Err(error) => Err(VaultError::Storage(error)),
        }
    }

    fn record_webdav_sync_state(
        &self,
        configuration: &SyncConfiguration,
        remote_etag: String,
    ) -> Result<()> {
        let expected_configuration = configuration.clone();
        self.transaction(move |data| {
            if data.sync_configuration.as_ref() != Some(&expected_configuration) {
                // The user changed sync settings while network I/O was in progress.  The remote
                // write is not acknowledged locally, so the next action can only fail safely
                // rather than reuse this ETag for a different endpoint.
                return Err(VaultError::SyncConflict);
            }
            data.webdav_sync_state = Some(WebDavSyncState::from_configuration(
                &expected_configuration,
                remote_etag,
            )?);
            Ok(())
        })
    }

    fn import_downloaded_sync_bundle(
        &self,
        bundle: EncryptedSyncBundle,
        password: &str,
        expected_configuration: &SyncConfiguration,
        expected_envelope: &VaultEnvelope,
    ) -> Result<VaultImportReceipt> {
        // Do not lock or touch the local vault until the remote bundle's password-derived key,
        // wrapped DEK, AEAD payload, and semantic data have all been authenticated.
        let preview = self.verify_encrypted_bundle(&bundle, password)?;
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let current = guard
            .unlocked
            .as_ref()
            .ok_or(VaultError::VaultChangedOnDisk)?;
        if current.envelope != *expected_envelope
            || current.data.sync_configuration.as_ref() != Some(expected_configuration)
        {
            return Err(VaultError::SyncConflict);
        }

        // The configuration snapshot is now known to match the still-unlocked local vault.  Drop
        // the runtime DEK before replacing its backing envelope so a later command cannot pair
        // stale plaintext with the imported ciphertext.
        guard.unlocked = None;
        let backup_created = self.back_up_current_envelope()?;
        write_envelope_atomic(&self.path, &bundle.vault)?;
        Ok(VaultImportReceipt {
            status: locked_vault_status(),
            encrypted_bundle_sha256: preview.encrypted_bundle_sha256,
            bytes: preview.bytes,
            backup_created,
            rollback_available: backup_created,
            rollback_path_hint: backup_created.then(|| import_backup_path_hint(&self.path)),
            restored_from_backup: false,
        })
    }

    fn vault_exists(&self) -> Result<bool> {
        match fs::metadata(&self.path) {
            Ok(metadata) => Ok(metadata.is_file()),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
            Err(error) => Err(VaultError::Storage(error)),
        }
    }

    fn read_unlocked<T>(&self, read: impl FnOnce(&VaultData) -> Result<T>) -> Result<T> {
        let guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let unlocked = guard.unlocked.as_ref().ok_or(VaultError::Locked)?;
        read(&unlocked.data)
    }

    /// Encrypt, write, and then commit a cloned candidate.  A failed disk write cannot leave the
    /// in-memory runtime ahead of the durable encrypted envelope.
    fn transaction<T>(&self, change: impl FnOnce(&mut VaultData) -> Result<T>) -> Result<T> {
        let mut guard = self.runtime.lock().map_err(|_| VaultError::LockPoisoned)?;
        let unlocked = guard.unlocked.as_mut().ok_or(VaultError::Locked)?;
        let mut candidate_data = unlocked.data.clone();
        let output = change(&mut candidate_data)?;
        candidate_data.validate()?;

        let mut candidate_envelope = unlocked.envelope.clone();
        candidate_envelope.encrypted_payload = encrypt_data(&unlocked.dek, &candidate_data)?;
        candidate_envelope.updated_at_unix_ms = now_unix_ms();
        write_envelope_atomic(&self.path, &candidate_envelope)?;

        unlocked.data = candidate_data;
        unlocked.envelope = candidate_envelope;
        Ok(output)
    }
}

fn validate_bundle_with_password(
    bundle: &EncryptedSyncBundle,
    password: &str,
) -> Result<VaultData> {
    bundle.validate()?;
    let dek = bundle.vault.unwrap_with_password(password)?;
    let data = decrypt_data(&dek, &bundle.vault.encrypted_payload)?;
    data.validate()?;
    Ok(data)
}

fn checked_bundle_bytes(bundle: &EncryptedSyncBundle) -> Result<Vec<u8>> {
    let bytes = bundle.to_bytes()?;
    if bytes.len() > crate::sync::MAX_SYNC_BUNDLE_BYTES {
        return Err(VaultError::SyncBundleTooLarge);
    }
    Ok(bytes)
}

fn import_backup_path(path: &Path) -> PathBuf {
    path.with_extension("json.bak")
}

fn import_backup_path_hint(path: &Path) -> String {
    import_backup_path(path)
        .file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_else(|| format!("{VAULT_FILENAME}.bak"))
}

fn locked_vault_status() -> VaultStatus {
    VaultStatus {
        exists: true,
        unlocked: false,
        format: Some(VAULT_FORMAT.to_owned()),
        vault_path_hint: VAULT_FILENAME.to_owned(),
    }
}

fn encrypt_data(dek: &[u8; DEK_BYTES], data: &VaultData) -> Result<EncryptedPayload> {
    let plaintext = Zeroizing::new(serde_json::to_vec(data).map_err(VaultError::Serialization)?);
    encrypt(dek, plaintext.as_slice(), &aad("vault-payload"))
}

fn decrypt_data(dek: &[u8; DEK_BYTES], encrypted_payload: &EncryptedPayload) -> Result<VaultData> {
    let plaintext = Zeroizing::new(decrypt(dek, encrypted_payload, &aad("vault-payload"))?);
    serde_json::from_slice(plaintext.as_slice()).map_err(|_| VaultError::Malformed)
}

fn read_envelope(path: &Path) -> Result<VaultEnvelope> {
    let bytes = fs::read(path).map_err(|error| {
        if error.kind() == std::io::ErrorKind::NotFound {
            VaultError::Missing
        } else {
            VaultError::Storage(error)
        }
    })?;
    let envelope: VaultEnvelope =
        serde_json::from_slice(&bytes).map_err(VaultError::Serialization)?;
    envelope.validate_structure()?;
    Ok(envelope)
}

fn write_envelope_atomic(path: &Path, envelope: &VaultEnvelope) -> Result<()> {
    let serialized = serde_json::to_vec_pretty(envelope).map_err(VaultError::Serialization)?;
    let parent = path.parent().ok_or_else(|| {
        VaultError::Storage(std::io::Error::other("vault path has no parent directory"))
    })?;
    fs::create_dir_all(parent).map_err(VaultError::Storage)?;

    let temporary = path.with_extension(format!("tmp-{}", Uuid::new_v4()));
    let write_result = (|| -> std::io::Result<()> {
        let mut file: File = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)?;
        file.write_all(&serialized)?;
        file.sync_all()?;
        drop(file);
        fs::rename(&temporary, path)?;
        Ok(())
    })();
    if let Err(error) = write_result {
        let _ = fs::remove_file(&temporary);
        return Err(VaultError::Storage(error));
    }
    Ok(())
}

fn now_unix_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis()
        .try_into()
        .unwrap_or(u64::MAX)
}

#[cfg(test)]
mod tests {
    use std::{fs, path::PathBuf};

    use super::*;
    use crate::domain::{
        ProfileAuthenticationInput, SshProfileInput, SyncConfigurationInput, TerminalPreferences,
    };

    const PASSWORD: &str = "a vault password with enough entropy";

    struct TestVault {
        root: PathBuf,
        service: VaultService,
    }

    impl TestVault {
        fn new() -> Self {
            let root = std::env::temp_dir().join(format!("nekossh-vault-test-{}", Uuid::new_v4()));
            let path = root.join(VAULT_FILENAME);
            Self {
                root,
                service: VaultService::new(path),
            }
        }
    }

    impl Drop for TestVault {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    fn profile_input() -> SshProfileInput {
        SshProfileInput {
            label: "Production database".to_owned(),
            host: "db.example.test".to_owned(),
            port: 2222,
            username: "deploy".to_owned(),
            authentication: ProfileAuthenticationInput::Password {
                password: "profile-password-must-not-reach-disk-plaintext".to_owned(),
            },
            tags: vec!["production".to_owned()],
            terminal: TerminalPreferences::default(),
        }
    }

    #[test]
    fn bootstrap_unlock_store_and_list_profiles_without_plaintext_on_disk() {
        let vault = TestVault::new();
        let status = vault.service.bootstrap(PASSWORD).expect("bootstrap");
        assert!(status.exists && status.unlocked);
        assert_eq!(status.format.as_deref(), Some(VAULT_FORMAT));

        let summary = vault
            .service
            .create_profile(profile_input())
            .expect("create profile");
        assert_eq!(summary.label, "Production database");
        assert_eq!(
            summary.authentication_kind,
            crate::domain::AuthenticationKind::Password
        );
        let summaries = vault.service.list_profiles().expect("list profiles");
        assert_eq!(summaries, vec![summary.clone()]);

        let raw_vault = fs::read_to_string(vault.service.path()).expect("read envelope");
        assert!(raw_vault.contains(VAULT_FORMAT));
        assert!(!raw_vault.contains("Production database"));
        assert!(!raw_vault.contains("profile-password-must-not-reach-disk-plaintext"));

        vault.service.lock().expect("lock");
        assert!(matches!(
            vault.service.list_profiles(),
            Err(VaultError::Locked)
        ));
        assert!(matches!(
            vault.service.unlock("incorrect password"),
            Err(VaultError::UnlockFailed)
        ));
        vault.service.unlock(PASSWORD).expect("unlock");
        assert_eq!(
            vault.service.list_profiles().expect("list after unlock"),
            vec![summary]
        );
    }

    #[test]
    fn password_change_rewraps_dek_and_preserves_profiles() {
        let vault = TestVault::new();
        vault.service.bootstrap(PASSWORD).expect("bootstrap");
        let profile = vault
            .service
            .create_profile(profile_input())
            .expect("profile");
        let old_envelope = read_envelope(vault.service.path()).expect("old envelope");
        let old_payload = old_envelope.encrypted_payload.clone();

        let new_password = "a different vault password with enough entropy";
        vault
            .service
            .change_password(PASSWORD, new_password)
            .expect("change password");
        let new_envelope = read_envelope(vault.service.path()).expect("new envelope");
        assert_eq!(
            new_envelope.encrypted_payload, old_payload,
            "password rotation must only rewrap DEK"
        );
        assert_ne!(new_envelope.wrapped_dek, old_envelope.wrapped_dek);

        vault.service.lock().expect("lock");
        assert!(matches!(
            vault.service.unlock(PASSWORD),
            Err(VaultError::UnlockFailed)
        ));
        vault
            .service
            .unlock(new_password)
            .expect("new password unlocks");
        assert_eq!(
            vault.service.list_profiles().expect("profiles survive"),
            vec![profile]
        );
    }

    #[test]
    fn password_change_refuses_a_vault_replaced_while_unlocked() {
        let vault = TestVault::new();
        vault.service.bootstrap(PASSWORD).expect("bootstrap");
        let mut changed_on_disk = read_envelope(vault.service.path()).expect("envelope");
        changed_on_disk.updated_at_unix_ms = changed_on_disk.updated_at_unix_ms.saturating_add(1);
        write_envelope_atomic(vault.service.path(), &changed_on_disk).expect("external write");

        assert!(matches!(
            vault
                .service
                .change_password(PASSWORD, "another strong enough vault password"),
            Err(VaultError::VaultChangedOnDisk)
        ));
    }

    #[test]
    fn encrypted_sync_bundle_and_plan_do_not_serialize_token() {
        let vault = TestVault::new();
        vault.service.bootstrap(PASSWORD).expect("bootstrap");
        vault
            .service
            .configure_sync(SyncConfigurationInput::Git {
                provider: crate::domain::GitProvider::Github,
                remote_url: "https://github.com/example/nekossh-backup.git".to_owned(),
                branch: "main".to_owned(),
                access_token: Some("github-token-must-never-be-plain".to_owned()),
            })
            .expect("configure sync");

        let bundle = vault.service.export_encrypted_bundle().expect("bundle");
        let encoded_bundle =
            String::from_utf8(bundle.to_bytes().expect("encode bundle")).expect("utf8 bundle");
        assert!(!encoded_bundle.contains("github-token-must-never-be-plain"));

        let plan = vault.service.sync_upload_plan().expect("plan");
        let encoded_plan = serde_json::to_string(&plan).expect("encode plan");
        assert!(!encoded_plan.contains("github-token-must-never-be-plain"));
        assert!(encoded_plan.contains("vault_git_token"));
    }

    #[test]
    fn malformed_or_swapped_format_is_rejected_before_decryption() {
        let vault = TestVault::new();
        vault.service.bootstrap(PASSWORD).expect("bootstrap");
        vault.service.lock().expect("lock");
        let mut envelope = read_envelope(vault.service.path()).expect("envelope");
        envelope.format = "different-format".to_owned();
        write_envelope_atomic(vault.service.path(), &envelope).expect("write tampered vault");
        assert!(matches!(
            vault.service.unlock(PASSWORD),
            Err(VaultError::UnsupportedFormat)
        ));
    }

    #[test]
    fn encrypted_bundle_import_authenticates_password_and_starts_locked() {
        let source = TestVault::new();
        source
            .service
            .bootstrap(PASSWORD)
            .expect("source bootstrap");
        let source_profile = source
            .service
            .create_profile(profile_input())
            .expect("source profile");
        let bundle = source
            .service
            .export_encrypted_bundle()
            .expect("export bundle");

        let destination = TestVault::new();
        let receipt = destination
            .service
            .import_encrypted_bundle(bundle, PASSWORD)
            .expect("import encrypted bundle");
        assert!(receipt.status.exists);
        assert!(!receipt.status.unlocked);
        assert!(!receipt.backup_created);
        assert!(matches!(
            destination.service.list_profiles(),
            Err(VaultError::Locked)
        ));
        destination
            .service
            .unlock(PASSWORD)
            .expect("unlock imported vault");
        assert_eq!(
            destination
                .service
                .list_profiles()
                .expect("imported profiles"),
            vec![source_profile]
        );
    }

    #[test]
    fn encrypted_bundle_import_requires_the_current_vault_to_be_locked() {
        let source = TestVault::new();
        source
            .service
            .bootstrap(PASSWORD)
            .expect("source bootstrap");
        let bundle = source
            .service
            .export_encrypted_bundle()
            .expect("export bundle");

        let destination = TestVault::new();
        destination
            .service
            .bootstrap(PASSWORD)
            .expect("destination bootstrap");
        assert!(matches!(
            destination
                .service
                .import_encrypted_bundle(bundle, PASSWORD),
            Err(VaultError::VaultMustBeLockedForImport)
        ));
    }

    #[test]
    fn encrypted_bundle_import_rejects_wrong_password_without_touching_local_vault() {
        let source = TestVault::new();
        source
            .service
            .bootstrap(PASSWORD)
            .expect("source bootstrap");
        let bundle = source
            .service
            .export_encrypted_bundle()
            .expect("source bundle");

        let destination = TestVault::new();
        destination
            .service
            .bootstrap(PASSWORD)
            .expect("destination bootstrap");
        destination
            .service
            .create_profile(profile_input())
            .expect("destination profile");
        destination.service.lock().expect("destination lock");
        let before = fs::read(destination.service.path()).expect("local vault bytes");

        assert!(matches!(
            destination
                .service
                .import_encrypted_bundle(bundle, "definitely-not-the-backup-password"),
            Err(VaultError::UnlockFailed)
        ));
        assert_eq!(
            fs::read(destination.service.path()).expect("local vault bytes after failure"),
            before
        );
        assert!(!import_backup_path(destination.service.path()).exists());
    }

    #[test]
    fn verified_import_preserves_a_rollback_copy_and_restore_requires_its_password() {
        const SOURCE_PASSWORD: &str = "a separate backup password with enough entropy";
        let source = TestVault::new();
        source
            .service
            .bootstrap(SOURCE_PASSWORD)
            .expect("source bootstrap");
        let source_profile = source
            .service
            .create_profile(profile_input())
            .expect("source profile");
        let bundle = source
            .service
            .export_encrypted_bundle()
            .expect("source bundle");

        let destination = TestVault::new();
        destination
            .service
            .bootstrap(PASSWORD)
            .expect("destination bootstrap");
        let mut local_profile = profile_input();
        local_profile.label = "Pre-import local profile".to_owned();
        let local_profile = destination
            .service
            .create_profile(local_profile)
            .expect("local profile");
        destination.service.lock().expect("destination lock");

        let preview = destination
            .service
            .verify_encrypted_bundle(&bundle, SOURCE_PASSWORD)
            .expect("verified preview");
        assert_eq!(preview.profile_count, 1);
        assert!(preview.encrypted_bundle_sha256.starts_with("sha256:"));

        let receipt = destination
            .service
            .import_encrypted_bundle(bundle, SOURCE_PASSWORD)
            .expect("verified import");
        assert!(receipt.backup_created);
        assert!(receipt.rollback_available);
        assert_eq!(
            receipt.rollback_path_hint.as_deref(),
            Some("neko233-v1.vault.json.bak")
        );
        assert!(import_backup_path(destination.service.path()).is_file());
        assert!(matches!(
            destination.service.unlock(PASSWORD),
            Err(VaultError::UnlockFailed)
        ));
        destination
            .service
            .unlock(SOURCE_PASSWORD)
            .expect("imported password unlocks");
        assert_eq!(
            destination
                .service
                .list_profiles()
                .expect("imported profile"),
            vec![source_profile]
        );
        destination.service.lock().expect("lock imported vault");

        assert!(matches!(
            destination
                .service
                .restore_import_backup("wrong rollback password"),
            Err(VaultError::UnlockFailed)
        ));
        let restore = destination
            .service
            .restore_import_backup(PASSWORD)
            .expect("restore rollback copy");
        assert!(restore.restored_from_backup);
        assert!(restore.rollback_available);
        destination
            .service
            .unlock(PASSWORD)
            .expect("original password unlocks restored vault");
        assert_eq!(
            destination
                .service
                .list_profiles()
                .expect("restored profile"),
            vec![local_profile]
        );
    }

    #[test]
    fn downloaded_webdav_restore_verifies_before_locking_or_replacing() {
        const SOURCE_PASSWORD: &str = "a remote restore password with enough entropy";
        let source = TestVault::new();
        source
            .service
            .bootstrap(SOURCE_PASSWORD)
            .expect("source bootstrap");
        let source_profile = source
            .service
            .create_profile(profile_input())
            .expect("source profile");
        let bundle = source
            .service
            .export_encrypted_bundle()
            .expect("source bundle");

        let destination = TestVault::new();
        destination
            .service
            .bootstrap(PASSWORD)
            .expect("destination bootstrap");
        let local_profile = destination
            .service
            .create_profile(profile_input())
            .expect("local profile");
        destination
            .service
            .configure_sync(SyncConfigurationInput::WebDav {
                endpoint: "https://dav.example.test/collection".to_owned(),
                remote_path: "nekossh/vault.bundle".to_owned(),
                username: Some("alice".to_owned()),
                secret: "webdav-token-not-in-preview".to_owned(),
            })
            .expect("WebDAV config");
        let (configuration, envelope) = {
            let guard = destination.service.runtime.lock().expect("runtime lock");
            let unlocked = guard.unlocked.as_ref().expect("unlocked destination");
            (
                unlocked
                    .data
                    .sync_configuration
                    .clone()
                    .expect("WebDAV configuration"),
                unlocked.envelope.clone(),
            )
        };

        assert!(matches!(
            destination.service.import_downloaded_sync_bundle(
                bundle.clone(),
                "wrong remote restore password",
                &configuration,
                &envelope,
            ),
            Err(VaultError::UnlockFailed)
        ));
        assert_eq!(
            destination
                .service
                .list_profiles()
                .expect("vault stays unlocked after failed verification"),
            vec![local_profile]
        );
        assert!(!import_backup_path(destination.service.path()).exists());

        let receipt = destination
            .service
            .import_downloaded_sync_bundle(bundle, SOURCE_PASSWORD, &configuration, &envelope)
            .expect("verified downloaded restore");
        assert!(receipt.backup_created);
        assert!(!receipt.status.unlocked);
        assert!(matches!(
            destination.service.list_profiles(),
            Err(VaultError::Locked)
        ));
        destination
            .service
            .unlock(SOURCE_PASSWORD)
            .expect("remote password unlocks restored vault");
        assert_eq!(
            destination
                .service
                .list_profiles()
                .expect("restored remote profile"),
            vec![source_profile]
        );
    }

    #[test]
    fn external_ssh_plan_uses_an_app_owned_known_hosts_file() {
        let vault = TestVault::new();
        vault.service.bootstrap(PASSWORD).expect("bootstrap");
        let profile = vault
            .service
            .create_profile(SshProfileInput {
                label: "Agent server".to_owned(),
                host: "agent.example.test".to_owned(),
                port: 22,
                username: "deploy".to_owned(),
                authentication: ProfileAuthenticationInput::Agent,
                tags: Vec::new(),
                terminal: TerminalPreferences::default(),
            })
            .expect("profile");
        let plan = vault
            .service
            .ssh_session_plan(profile.id)
            .expect("SSH session plan");
        let invocation = plan.external_ssh.expect("external SSH invocation");
        let known_hosts_path = vault.root.join(SSH_KNOWN_HOSTS_FILENAME);
        assert!(known_hosts_path.is_file());
        assert!(invocation.arguments.iter().any(|argument| {
            argument == &format!("UserKnownHostsFile={}", known_hosts_path.display())
        }));
        assert!(
            invocation
                .arguments
                .iter()
                .any(|argument| argument == "StrictHostKeyChecking=accept-new")
        );
        assert!(
            invocation
                .arguments
                .iter()
                .any(|argument| argument == "GlobalKnownHostsFile=none")
        );
    }
}
