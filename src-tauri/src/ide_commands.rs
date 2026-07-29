//! Native Tauri v2 SSH IDE commands.
//!
//! Rust connects through the in-process `ssh2` library. No system `ssh.exe`, PuTTY, plink,
//! external terminal, Vault, sync provider, or localhost Web backend participates.

use std::sync::Arc;

use serde::Serialize;
use tauri::{AppHandle, State};
use uuid::Uuid;

use crate::{
    domain::SshProfileInput,
    ephemeral::{EphemeralConnectionStore, EphemeralConnectionSummary},
    error::VaultError,
    sftp_transfer::{
        SftpTransferService, SshSftpDownloadInput, SshSftpTransferStarted, SshSftpUploadInput,
    },
    ssh::{SshCommandResult, SshDirectoryListing, SshHostKeyStatus, SshService},
    ssh_terminal::{SshTerminalStarted, TerminalService},
};

pub struct IdeState {
    owner: Uuid,
    store: Arc<EphemeralConnectionStore>,
    ssh: Arc<SshService>,
    transfer: Arc<SftpTransferService>,
    terminal: Arc<TerminalService>,
}

impl IdeState {
    pub fn new() -> Self {
        let store = Arc::new(EphemeralConnectionStore::new());
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        Self {
            owner: Uuid::new_v4(),
            transfer: Arc::new(SftpTransferService::new(Arc::clone(&ssh))),
            terminal: Arc::new(TerminalService::new(Arc::clone(&ssh))),
            ssh,
            store,
        }
    }
}

impl Default for IdeState {
    fn default() -> Self {
        Self::new()
    }
}

impl Drop for IdeState {
    fn drop(&mut self) {
        let _ = self.transfer.cancel_all();
        let _ = self.terminal.close_all();
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct IdeCommandError {
    pub code: String,
    pub message: String,
}

impl From<VaultError> for IdeCommandError {
    fn from(error: VaultError) -> Self {
        Self {
            code: error.code().to_owned(),
            message: error.public_message(),
        }
    }
}

type CommandResult<T> = std::result::Result<T, IdeCommandError>;

#[tauri::command]
pub fn ide_connection_create(
    state: State<'_, IdeState>,
    input: SshProfileInput,
) -> CommandResult<EphemeralConnectionSummary> {
    state.store.create(state.owner, input).map_err(Into::into)
}

#[tauri::command]
pub fn ide_connection_list(
    state: State<'_, IdeState>,
) -> CommandResult<Vec<EphemeralConnectionSummary>> {
    state.store.browser_list(state.owner).map_err(Into::into)
}

#[tauri::command]
pub fn ide_connection_disconnect(
    state: State<'_, IdeState>,
    connection_id: Uuid,
) -> CommandResult<()> {
    state
        .terminal
        .close_for_profile(connection_id)
        .map_err(IdeCommandError::from)?;
    state
        .transfer
        .cancel_for_profile(connection_id)
        .map_err(IdeCommandError::from)?;
    state
        .ssh
        .forget_profile(connection_id)
        .map_err(IdeCommandError::from)?;
    state
        .store
        .remove_browser_connection(state.owner, connection_id)
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_ssh_prepare(
    state: State<'_, IdeState>,
    connection_id: Uuid,
) -> CommandResult<SshHostKeyStatus> {
    touch(&state, connection_id)?;
    let ssh = Arc::clone(&state.ssh);
    tauri::async_runtime::spawn_blocking(move || ssh.prepare(connection_id))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshConnectionFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_ssh_trust_host_key(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    confirmation_id: Uuid,
) -> CommandResult<SshHostKeyStatus> {
    touch(&state, connection_id)?;
    let ssh = Arc::clone(&state.ssh);
    tauri::async_runtime::spawn_blocking(move || ssh.trust_host_key(connection_id, confirmation_id))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshHostKeyConfirmationExpired))?
        .map_err(Into::into)
}

#[tauri::command]
pub fn ide_ssh_reject_host_key(
    state: State<'_, IdeState>,
    confirmation_id: Uuid,
) -> CommandResult<()> {
    state
        .ssh
        .reject_host_key(confirmation_id)
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_ssh_exec(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    command: String,
    cwd: Option<String>,
) -> CommandResult<SshCommandResult> {
    touch(&state, connection_id)?;
    let ssh = Arc::clone(&state.ssh);
    tauri::async_runtime::spawn_blocking(move || ssh.execute(connection_id, command, cwd))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshCommandFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_list(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<SshDirectoryListing> {
    touch(&state, connection_id)?;
    let ssh = Arc::clone(&state.ssh);
    tauri::async_runtime::spawn_blocking(move || ssh.list_directory(connection_id, path))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshDirectoryListFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_upload_start(
    app: AppHandle,
    state: State<'_, IdeState>,
    connection_id: Uuid,
    remote_directory: String,
    files: Vec<SshSftpUploadInput>,
) -> CommandResult<SshSftpTransferStarted> {
    touch(&state, connection_id)?;
    let transfer = Arc::clone(&state.transfer);
    tauri::async_runtime::spawn_blocking(move || {
        transfer.start_upload(app, connection_id, remote_directory, files)
    })
    .await
    .map_err(|_| IdeCommandError::from(VaultError::SshTransferFailed))?
    .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_download_start(
    app: AppHandle,
    state: State<'_, IdeState>,
    connection_id: Uuid,
    files: Vec<SshSftpDownloadInput>,
) -> CommandResult<SshSftpTransferStarted> {
    touch(&state, connection_id)?;
    let transfer = Arc::clone(&state.transfer);
    tauri::async_runtime::spawn_blocking(move || transfer.start_download(app, connection_id, files))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshTransferFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub fn ide_sftp_transfer_cancel(state: State<'_, IdeState>, job_id: Uuid) -> CommandResult<()> {
    state.transfer.cancel(job_id).map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_mkdir(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<()> {
    touch(&state, connection_id)?;
    let transfer = Arc::clone(&state.transfer);
    tauri::async_runtime::spawn_blocking(move || transfer.create_directory(connection_id, path))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshTransferFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_rename(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    from_path: String,
    to_path: String,
    overwrite: bool,
) -> CommandResult<()> {
    touch(&state, connection_id)?;
    let transfer = Arc::clone(&state.transfer);
    tauri::async_runtime::spawn_blocking(move || {
        transfer.rename_remote(connection_id, from_path, to_path, overwrite)
    })
    .await
    .map_err(|_| IdeCommandError::from(VaultError::SshTransferFailed))?
    .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_delete(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<()> {
    touch(&state, connection_id)?;
    let transfer = Arc::clone(&state.transfer);
    tauri::async_runtime::spawn_blocking(move || transfer.delete_remote(connection_id, path))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshTransferFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_terminal_start(
    app: AppHandle,
    state: State<'_, IdeState>,
    connection_id: Uuid,
    columns: Option<u32>,
    rows: Option<u32>,
) -> CommandResult<SshTerminalStarted> {
    touch(&state, connection_id)?;
    let terminal = Arc::clone(&state.terminal);
    tauri::async_runtime::spawn_blocking(move || terminal.start(app, connection_id, columns, rows))
        .await
        .map_err(|_| IdeCommandError::from(VaultError::SshTerminalStartFailed))?
        .map_err(Into::into)
}

#[tauri::command]
pub fn ide_terminal_write(
    state: State<'_, IdeState>,
    session_id: Uuid,
    data: String,
) -> CommandResult<()> {
    state.terminal.write(session_id, data).map_err(Into::into)
}

#[tauri::command]
pub fn ide_terminal_resize(
    state: State<'_, IdeState>,
    session_id: Uuid,
    columns: u32,
    rows: u32,
) -> CommandResult<()> {
    state
        .terminal
        .resize(session_id, columns, rows)
        .map_err(Into::into)
}

#[tauri::command]
pub fn ide_terminal_close(state: State<'_, IdeState>, session_id: Uuid) -> CommandResult<()> {
    state.terminal.close(session_id).map_err(Into::into)
}

fn touch(state: &State<'_, IdeState>, connection_id: Uuid) -> CommandResult<()> {
    state
        .store
        .browser_connection(state.owner, connection_id)
        .map(|_| ())
        .map_err(Into::into)
}
