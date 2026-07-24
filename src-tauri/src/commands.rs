//! Narrow Tauri IPC surface for encrypted-vault operations.
//!
//! Commands return summaries/plans only.  No command returns a profile password, private key,
//! WebDAV secret, or Git token; encrypted backup bundles are safe to move through IPC because
//! they contain the encrypted envelope only.

use std::{path::PathBuf, sync::Arc};

use serde::Serialize;
use tauri::{AppHandle, State};
use tauri_plugin_opener::OpenerExt;
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    domain::{
        ProfileSummary, SshProfileInput, SshProfilePatch, SshSessionPlan, SyncConfigurationInput,
        SyncConfigurationSummary, validate_external_browser_url,
    },
    error::VaultError,
    sync::{EncryptedSyncBundle, SyncTransferPlan, SyncTransferReceipt},
    vault::{VaultImportReceipt, VaultService, VaultStatus, VerifiedSyncBundle},
};

pub struct VaultState {
    service: Arc<VaultService>,
}

impl VaultState {
    pub fn new(vault_path: PathBuf) -> Self {
        Self {
            service: Arc::new(VaultService::new(vault_path)),
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CommandError {
    pub code: String,
    pub message: String,
}

impl From<VaultError> for CommandError {
    fn from(error: VaultError) -> Self {
        Self {
            code: error.code().to_owned(),
            message: error.public_message(),
        }
    }
}

type CommandResult<T> = std::result::Result<T, CommandError>;

#[tauri::command]
pub fn vault_status(state: State<'_, VaultState>) -> CommandResult<VaultStatus> {
    state.service.status().map_err(Into::into)
}

#[tauri::command]
pub fn vault_bootstrap(
    state: State<'_, VaultState>,
    password: String,
) -> CommandResult<VaultStatus> {
    let password = Zeroizing::new(password);
    state
        .service
        .bootstrap(password.as_str())
        .map_err(Into::into)
}

#[tauri::command]
pub fn vault_unlock(state: State<'_, VaultState>, password: String) -> CommandResult<VaultStatus> {
    let password = Zeroizing::new(password);
    state.service.unlock(password.as_str()).map_err(Into::into)
}

#[tauri::command]
pub fn vault_lock(state: State<'_, VaultState>) -> CommandResult<VaultStatus> {
    state.service.lock().map_err(Into::into)
}

#[tauri::command]
pub fn vault_change_password(
    state: State<'_, VaultState>,
    current_password: String,
    new_password: String,
) -> CommandResult<()> {
    let current_password = Zeroizing::new(current_password);
    let new_password = Zeroizing::new(new_password);
    state
        .service
        .change_password(current_password.as_str(), new_password.as_str())
        .map_err(Into::into)
}

#[tauri::command]
pub fn profile_create(
    state: State<'_, VaultState>,
    input: SshProfileInput,
) -> CommandResult<ProfileSummary> {
    state.service.create_profile(input).map_err(Into::into)
}

#[tauri::command]
pub fn profile_list(state: State<'_, VaultState>) -> CommandResult<Vec<ProfileSummary>> {
    state.service.list_profiles().map_err(Into::into)
}

#[tauri::command]
pub fn profile_get(
    state: State<'_, VaultState>,
    profile_id: Uuid,
) -> CommandResult<ProfileSummary> {
    state.service.get_profile(profile_id).map_err(Into::into)
}

#[tauri::command]
pub fn ssh_session_plan(
    state: State<'_, VaultState>,
    profile_id: Uuid,
) -> CommandResult<SshSessionPlan> {
    state
        .service
        .ssh_session_plan(profile_id)
        .map_err(Into::into)
}

/// Launches a visible traditional SSH terminal without a shell.  On Windows this starts a new
/// Windows Terminal tab, so the returned PID belongs to the terminal launcher rather than an
/// embedded SSH/PTY session.  Generated arguments never include a vaulted password, private-key
/// PEM, WebDAV secret, or Git token.
#[tauri::command]
pub fn ssh_launch_external(state: State<'_, VaultState>, profile_id: Uuid) -> CommandResult<u32> {
    state
        .service
        .launch_external_ssh(profile_id)
        .map_err(Into::into)
}

#[tauri::command]
pub fn profile_update(
    state: State<'_, VaultState>,
    profile_id: Uuid,
    patch: SshProfilePatch,
) -> CommandResult<ProfileSummary> {
    state
        .service
        .update_profile(profile_id, patch)
        .map_err(Into::into)
}

#[tauri::command]
pub fn profile_delete(state: State<'_, VaultState>, profile_id: Uuid) -> CommandResult<()> {
    state.service.delete_profile(profile_id).map_err(Into::into)
}

#[tauri::command]
pub fn sync_configure(
    state: State<'_, VaultState>,
    input: SyncConfigurationInput,
) -> CommandResult<SyncConfigurationSummary> {
    state.service.configure_sync(input).map_err(Into::into)
}

#[tauri::command]
pub fn sync_configuration(
    state: State<'_, VaultState>,
) -> CommandResult<Option<SyncConfigurationSummary>> {
    state.service.sync_configuration().map_err(Into::into)
}

#[tauri::command]
pub fn sync_export_encrypted_bundle(
    state: State<'_, VaultState>,
) -> CommandResult<EncryptedSyncBundle> {
    state.service.export_encrypted_bundle().map_err(Into::into)
}

/// Authenticates a downloaded encrypted backup before the UI offers a destructive replacement.
/// The supplied password is zeroized at the Tauri boundary and no vault file is changed.
#[tauri::command]
pub fn sync_verify_encrypted_bundle(
    state: State<'_, VaultState>,
    bundle: EncryptedSyncBundle,
    password: String,
) -> CommandResult<VerifiedSyncBundle> {
    let password = Zeroizing::new(password);
    state
        .service
        .verify_encrypted_bundle(&bundle, password.as_str())
        .map_err(Into::into)
}

/// Imports an encrypted vault only after authenticating it with the supplied backup password.
/// The current encrypted vault is preserved as a local `.bak` rollback artifact first, and the
/// replacement intentionally remains locked.
#[tauri::command]
pub fn sync_import_encrypted_bundle(
    state: State<'_, VaultState>,
    bundle: EncryptedSyncBundle,
    password: String,
) -> CommandResult<VaultImportReceipt> {
    let password = Zeroizing::new(password);
    state
        .service
        .import_encrypted_bundle(bundle, password.as_str())
        .map_err(Into::into)
}

/// Restores the retained pre-import encrypted vault after the user supplies its password.  This
/// never exposes the backup path or its contents to the webview.
#[tauri::command]
pub fn sync_restore_import_backup(
    state: State<'_, VaultState>,
    password: String,
) -> CommandResult<VaultImportReceipt> {
    let password = Zeroizing::new(password);
    state
        .service
        .restore_import_backup(password.as_str())
        .map_err(Into::into)
}

#[tauri::command]
pub fn sync_upload_plan(state: State<'_, VaultState>) -> CommandResult<SyncTransferPlan> {
    state.service.sync_upload_plan().map_err(Into::into)
}

/// Runs WebDAV PUT work off the Tauri UI thread.  The service exports only an encrypted envelope
/// and never returns the configured credential to the webview.
#[tauri::command]
pub async fn sync_webdav_upload(
    state: State<'_, VaultState>,
) -> CommandResult<SyncTransferReceipt> {
    let service = Arc::clone(&state.service);
    tauri::async_runtime::spawn_blocking(move || service.sync_upload_webdav())
        .await
        .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
        .map_err(Into::into)
}

/// Downloads an encrypted WebDAV bundle for the UI's explicit import/replace confirmation step.
#[tauri::command]
pub async fn sync_webdav_download_encrypted_bundle(
    state: State<'_, VaultState>,
) -> CommandResult<EncryptedSyncBundle> {
    let service = Arc::clone(&state.service);
    tauri::async_runtime::spawn_blocking(move || service.sync_download_webdav_bundle())
        .await
        .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
        .map_err(Into::into)
}

/// Performs a real HTTPS Git upload through the in-process libgit2 transport.  The encrypted
/// vault token is consumed only by the backend credential callback; this command returns the same
/// non-sensitive receipt shape as WebDAV when the non-force push succeeds.
#[tauri::command]
pub async fn sync_git_upload(state: State<'_, VaultState>) -> CommandResult<SyncTransferReceipt> {
    let service = Arc::clone(&state.service);
    tauri::async_runtime::spawn_blocking(move || service.sync_upload_git())
        .await
        .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
        .map_err(Into::into)
}

/// Downloads only the encrypted Git artifact.  Frontends must follow it with explicit password
/// verification and the existing lock-and-import command before replacing local vault data.
#[tauri::command]
pub async fn sync_git_download_encrypted_bundle(
    state: State<'_, VaultState>,
) -> CommandResult<EncryptedSyncBundle> {
    let service = Arc::clone(&state.service);
    tauri::async_runtime::spawn_blocking(move || service.sync_download_git_bundle())
        .await
        .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
        .map_err(Into::into)
}

/// Restores a Git backup wholly in Rust: download, password/AEAD verification, rollback snapshot,
/// lock, and atomic import.  The encrypted bundle never passes through the webview.
#[tauri::command]
pub async fn sync_git_restore_import(
    state: State<'_, VaultState>,
    password: String,
) -> CommandResult<VaultImportReceipt> {
    let service = Arc::clone(&state.service);
    let password = Zeroizing::new(password);
    tauri::async_runtime::spawn_blocking(move || service.sync_git_restore_import(password.as_str()))
        .await
        .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
        .map_err(Into::into)
}

/// Downloads and restores the configured WebDAV backup entirely in Rust.  The caller supplies
/// the backup password, but the encrypted bundle itself never makes a browser IPC round-trip.
#[tauri::command]
pub async fn sync_webdav_restore_import(
    state: State<'_, VaultState>,
    password: String,
) -> CommandResult<VaultImportReceipt> {
    let service = Arc::clone(&state.service);
    let password = Zeroizing::new(password);
    tauri::async_runtime::spawn_blocking(move || {
        service.sync_webdav_restore_import(password.as_str())
    })
    .await
    .map_err(|_| CommandError::from(VaultError::SyncTransportFailed))?
    .map_err(Into::into)
}

/// Opens only HTTPS (or loopback HTTP) URLs in the user's default browser.  The URL is validated
/// before crossing into the OS shell, preventing arbitrary custom-protocol invocation.
#[tauri::command]
pub fn browser_open_url(app: AppHandle, url: String) -> CommandResult<()> {
    let url = validate_external_browser_url(&url).map_err(CommandError::from)?;
    app.opener()
        .open_url(url, None::<String>)
        .map_err(|_| CommandError::from(VaultError::BrowserOpenFailed))
}
