//! Native Tauri v2 SSH IDE commands.
//!
//! Rust connects through the in-process `ssh2` library. No system `ssh.exe`, PuTTY, plink,
//! external terminal, persistent credential store, sync provider, or localhost Web backend
//! participates.

use std::{
    sync::{Arc, Weak},
    thread,
    time::Duration,
};

use serde::Serialize;
use tauri::{AppHandle, State, ipc::Channel};
use uuid::Uuid;

use crate::{
    domain::SshProfileInput,
    ephemeral::{EphemeralConnectionStore, EphemeralConnectionSummary},
    error::NekoError,
    operation_admission::{AdmissionError, OperationAdmission, OperationPermit, ProfileCloseGuard},
    sftp_transfer::{
        SftpTransferService, SshSftpDownloadEvent, SshSftpDownloadInput, SshSftpTransferStarted,
        SshSftpUploadEvent, SshSftpUploadInput,
    },
    ssh::{SshCommandResult, SshDirectoryListing, SshHostKeyStatus, SshService},
    ssh_terminal::{SshTerminalEvent, SshTerminalStarted, TerminalService},
};

const EXPIRED_CONNECTION_SCAN_INTERVAL: Duration = Duration::from_secs(15);
const PROFILE_OPERATION_DRAIN_TIMEOUT: Duration = Duration::from_secs(5);

pub struct IdeState {
    owner: Uuid,
    store: Arc<EphemeralConnectionStore>,
    ssh: Arc<SshService>,
    transfer: Arc<SftpTransferService>,
    terminal: Arc<TerminalService>,
    admission: Arc<OperationAdmission>,
}

impl IdeState {
    pub fn new() -> Self {
        let store = Arc::new(EphemeralConnectionStore::new());
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        let transfer = Arc::new(SftpTransferService::new(Arc::clone(&ssh)));
        let terminal = Arc::new(TerminalService::new(Arc::clone(&ssh)));
        let admission = Arc::new(OperationAdmission::new());
        spawn_expired_connection_reaper(
            Arc::downgrade(&store),
            Arc::downgrade(&ssh),
            Arc::downgrade(&transfer),
            Arc::downgrade(&terminal),
            Arc::downgrade(&admission),
        );
        Self {
            owner: Uuid::new_v4(),
            transfer,
            terminal,
            admission,
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
        self.admission.shutdown();
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

impl From<NekoError> for IdeCommandError {
    fn from(error: NekoError) -> Self {
        Self {
            code: error.code().to_owned(),
            message: error.public_message(),
        }
    }
}

impl From<AdmissionError> for IdeCommandError {
    fn from(error: AdmissionError) -> Self {
        match error {
            AdmissionError::Busy => Self {
                code: "ssh_operation_busy".to_owned(),
                message: "当前 SSH 操作较多，请稍后重试。".to_owned(),
            },
            AdmissionError::Closing => Self {
                code: "ssh_connection_closing".to_owned(),
                message: "连接正在断开或已过期。".to_owned(),
            },
            AdmissionError::Unavailable => Self {
                code: "ssh_operation_admission_unavailable".to_owned(),
                message: "SSH 操作队列暂时不可用，请重试。".to_owned(),
            },
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
pub async fn ide_connection_disconnect(
    state: State<'_, IdeState>,
    connection_id: Uuid,
) -> CommandResult<()> {
    // Validate ownership before affecting any profile-scoped terminal or transfer resource.
    touch(&state, connection_id)?;
    let close_guard = state
        .admission
        .begin_close(connection_id)
        .map_err(IdeCommandError::from)?;
    let owner = state.owner;
    let store = Arc::clone(&state.store);
    let ssh = Arc::clone(&state.ssh);
    let transfer = Arc::clone(&state.transfer);
    let terminal = Arc::clone(&state.terminal);
    tauri::async_runtime::spawn_blocking(move || {
        retire_profile_resources(
            connection_id,
            close_guard,
            &ssh,
            &transfer,
            &terminal,
            Some((&store, owner)),
        )
    })
    .await
    .map_err(|_| IdeCommandError::from(NekoError::LockPoisoned))?
    .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_ssh_prepare(
    state: State<'_, IdeState>,
    connection_id: Uuid,
) -> CommandResult<SshHostKeyStatus> {
    let ssh = Arc::clone(&state.ssh);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshConnectionFailed,
        move |_| ssh.prepare(connection_id),
    )
    .await
}

#[tauri::command]
pub async fn ide_ssh_trust_host_key(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    confirmation_id: Uuid,
) -> CommandResult<SshHostKeyStatus> {
    let ssh = Arc::clone(&state.ssh);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshHostKeyConfirmationExpired,
        move |_| ssh.trust_host_key(connection_id, confirmation_id),
    )
    .await
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
    let ssh = Arc::clone(&state.ssh);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshCommandFailed,
        move |permit| ssh.execute(connection_id, command, cwd, permit.close_signal()),
    )
    .await
}

#[tauri::command]
pub async fn ide_sftp_list(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<SshDirectoryListing> {
    let ssh = Arc::clone(&state.ssh);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshDirectoryListFailed,
        move |_| ssh.list_directory(connection_id, path),
    )
    .await
}

#[tauri::command]
pub async fn ide_sftp_upload_start(
    app: AppHandle,
    state: State<'_, IdeState>,
    connection_id: Uuid,
    remote_directory: String,
    files: Vec<SshSftpUploadInput>,
    events: Channel<SshSftpUploadEvent>,
) -> CommandResult<SshSftpTransferStarted> {
    let transfer = Arc::clone(&state.transfer);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTransferFailed,
        move |permit| {
            let started = transfer.start_upload(
                app,
                connection_id,
                remote_directory,
                files,
                events,
                permit.clone(),
            )?;
            if permit.profile_is_closing() {
                let _ = transfer.cancel(started.job_id);
            }
            Ok(started)
        },
    )
    .await
}

#[tauri::command]
pub async fn ide_sftp_download_start(
    app: AppHandle,
    state: State<'_, IdeState>,
    connection_id: Uuid,
    files: Vec<SshSftpDownloadInput>,
    events: Channel<SshSftpDownloadEvent>,
) -> CommandResult<SshSftpTransferStarted> {
    let transfer = Arc::clone(&state.transfer);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTransferFailed,
        move |permit| {
            let started =
                transfer.start_download(app, connection_id, files, events, permit.clone())?;
            if permit.profile_is_closing() {
                let _ = transfer.cancel(started.job_id);
            }
            Ok(started)
        },
    )
    .await
}

#[tauri::command]
pub fn ide_sftp_transfer_cancel(state: State<'_, IdeState>, job_id: Uuid) -> CommandResult<()> {
    state.transfer.cancel(job_id).map_err(Into::into)
}

#[tauri::command]
pub fn ide_sftp_download_ack(
    state: State<'_, IdeState>,
    job_id: Uuid,
    sequence: u64,
) -> CommandResult<()> {
    let connection_id = state
        .transfer
        .profile_id_for_job(job_id)
        .map_err(IdeCommandError::from)?;
    touch(&state, connection_id)?;
    state
        .transfer
        .acknowledge_download(job_id, sequence)
        .map_err(Into::into)
}

#[tauri::command]
pub async fn ide_sftp_mkdir(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<()> {
    let transfer = Arc::clone(&state.transfer);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTransferFailed,
        move |permit| transfer.create_directory(connection_id, path, permit.close_signal()),
    )
    .await
}

#[tauri::command]
pub async fn ide_sftp_rename(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    from_path: String,
    to_path: String,
    overwrite: bool,
) -> CommandResult<()> {
    let transfer = Arc::clone(&state.transfer);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTransferFailed,
        move |permit| {
            transfer.rename_remote(
                connection_id,
                from_path,
                to_path,
                overwrite,
                permit.close_signal(),
            )
        },
    )
    .await
}

#[tauri::command]
pub async fn ide_sftp_delete(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    path: String,
) -> CommandResult<()> {
    let transfer = Arc::clone(&state.transfer);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTransferFailed,
        move |permit| transfer.delete_remote(connection_id, path, permit.close_signal()),
    )
    .await
}

#[tauri::command]
pub async fn ide_terminal_start(
    state: State<'_, IdeState>,
    connection_id: Uuid,
    columns: Option<u32>,
    rows: Option<u32>,
    events: Channel<SshTerminalEvent>,
) -> CommandResult<SshTerminalStarted> {
    let terminal = Arc::clone(&state.terminal);
    run_profile_operation(
        &state,
        connection_id,
        NekoError::SshTerminalStartFailed,
        move |permit| {
            let started =
                terminal.start(connection_id, columns, rows, events, permit.close_signal())?;
            if permit.profile_is_closing() {
                let _ = terminal.close(started.session_id);
            }
            Ok(started)
        },
    )
    .await
}

#[tauri::command]
pub fn ide_terminal_write(
    state: State<'_, IdeState>,
    session_id: Uuid,
    data: String,
) -> CommandResult<()> {
    let connection_id = state
        .terminal
        .profile_id_for_session(session_id)
        .map_err(IdeCommandError::from)?;
    run_profile_activity(&state, connection_id, || {
        state.terminal.write(session_id, data)
    })
}

#[tauri::command]
pub fn ide_terminal_resize(
    state: State<'_, IdeState>,
    session_id: Uuid,
    columns: u32,
    rows: u32,
) -> CommandResult<()> {
    let connection_id = state
        .terminal
        .profile_id_for_session(session_id)
        .map_err(IdeCommandError::from)?;
    run_profile_activity(&state, connection_id, || {
        state.terminal.resize(session_id, columns, rows)
    })
}

#[tauri::command]
pub fn ide_terminal_ack_output(
    state: State<'_, IdeState>,
    session_id: Uuid,
    sequence: u64,
) -> CommandResult<()> {
    let connection_id = state
        .terminal
        .profile_id_for_session(session_id)
        .map_err(IdeCommandError::from)?;
    run_profile_activity(&state, connection_id, || {
        state.terminal.acknowledge_output(session_id, sequence)
    })
}

#[tauri::command]
pub fn ide_terminal_close(state: State<'_, IdeState>, session_id: Uuid) -> CommandResult<()> {
    state.terminal.close(session_id).map_err(Into::into)
}

async fn run_profile_operation<T, F>(
    state: &State<'_, IdeState>,
    connection_id: Uuid,
    join_error: NekoError,
    operation: F,
) -> CommandResult<T>
where
    T: Send + 'static,
    F: FnOnce(&OperationPermit) -> crate::error::Result<T> + Send + 'static,
{
    // Admission comes first: a closing profile must not be revived by an activity touch.
    let permit = state
        .admission
        .try_acquire(connection_id)
        .map_err(IdeCommandError::from)?;
    touch(state, connection_id)?;
    tauri::async_runtime::spawn_blocking(move || execute_admitted_operation(&permit, operation))
        .await
        .map_err(|_| IdeCommandError::from(join_error))?
        .map_err(Into::into)
}

fn execute_admitted_operation<T, F>(
    permit: &OperationPermit,
    operation: F,
) -> crate::error::Result<T>
where
    F: FnOnce(&OperationPermit) -> crate::error::Result<T>,
{
    // A permit can wait in Tauri's blocking pool while disconnect closes the profile gate.
    // Recheck before entering any network service so queued work cannot start after closing.
    if permit.profile_is_closing() {
        return Err(NekoError::ProfileNotFound);
    }
    let result = operation(permit);
    if permit.profile_is_closing() {
        Err(NekoError::ProfileNotFound)
    } else {
        result
    }
}

fn run_profile_activity<T, F>(
    state: &State<'_, IdeState>,
    connection_id: Uuid,
    activity: F,
) -> CommandResult<T>
where
    F: FnOnce() -> crate::error::Result<T>,
{
    // Terminal input/resize is synchronous, but it can still race the gap between begin_close and
    // profile removal. Keep a permit across both the activity touch and the bounded queue write.
    let permit = state
        .admission
        .try_acquire_activity(connection_id)
        .map_err(IdeCommandError::from)?;
    touch(state, connection_id)?;
    execute_admitted_operation(&permit, |_| activity()).map_err(Into::into)
}

fn touch(state: &State<'_, IdeState>, connection_id: Uuid) -> CommandResult<()> {
    state
        .store
        .browser_connection(state.owner, connection_id)
        .map(|_| ())
        .map_err(Into::into)
}

fn spawn_expired_connection_reaper(
    store: Weak<EphemeralConnectionStore>,
    ssh: Weak<SshService>,
    transfer: Weak<SftpTransferService>,
    terminal: Weak<TerminalService>,
    admission: Weak<OperationAdmission>,
) {
    thread::Builder::new()
        .name("nekossh-connection-reaper".to_owned())
        .spawn(move || {
            loop {
                thread::sleep(EXPIRED_CONNECTION_SCAN_INTERVAL);
                let Some(admission) = admission.upgrade() else {
                    break;
                };
                if admission.is_shutdown() {
                    break;
                }
                let (Some(store), Some(ssh), Some(transfer), Some(terminal)) = (
                    store.upgrade(),
                    ssh.upgrade(),
                    transfer.upgrade(),
                    terminal.upgrade(),
                ) else {
                    break;
                };
                reap_expired_connections(&store, &ssh, &transfer, &terminal, &admission);
            }
        })
        .expect("failed to start the ephemeral SSH connection reaper");
}

fn reap_expired_connections(
    store: &Arc<EphemeralConnectionStore>,
    ssh: &Arc<SshService>,
    transfer: &Arc<SftpTransferService>,
    terminal: &Arc<TerminalService>,
    admission: &Arc<OperationAdmission>,
) {
    let Ok(expired_ids) = store.expired_connection_ids() else {
        return;
    };
    for profile_id in expired_ids {
        let Ok(close_guard) = admission.begin_close(profile_id) else {
            // An explicit disconnect already owns this profile's retirement.
            continue;
        };
        match store.remove_if_expired(profile_id) {
            Ok(true) => {
                let _ = retire_profile_resources(
                    profile_id,
                    close_guard,
                    ssh,
                    transfer,
                    terminal,
                    None,
                );
            }
            Ok(false) | Err(_) => {
                // Activity won the race. Dropping the guard reopens the still-live profile.
                drop(close_guard);
            }
        }
    }
}

fn retire_profile_resources(
    profile_id: Uuid,
    close_guard: ProfileCloseGuard,
    ssh: &SshService,
    transfer: &SftpTransferService,
    terminal: &TerminalService,
    remove_connection: Option<(&EphemeralConnectionStore, Uuid)>,
) -> crate::error::Result<()> {
    let mut first_error = None;

    // Cancel persistent resources immediately, then repeat after the bounded drain to catch a
    // terminal/transfer start that had already passed admission.
    remember_first_error(&mut first_error, terminal.close_for_profile(profile_id));
    remember_first_error(&mut first_error, transfer.cancel_for_profile(profile_id));
    let _ = close_guard.wait_for_idle(PROFILE_OPERATION_DRAIN_TIMEOUT);
    remember_first_error(&mut first_error, terminal.close_for_profile(profile_id));
    remember_first_error(&mut first_error, transfer.cancel_for_profile(profile_id));
    remember_first_error(&mut first_error, ssh.forget_profile(profile_id));
    if let Some((store, owner)) = remove_connection {
        remember_first_error(
            &mut first_error,
            store.remove_browser_connection(owner, profile_id),
        );
    }

    // Keep the closing tombstone if an admitted network operation outlives the bounded wait.
    close_guard.retire();
    match first_error {
        Some(error) => Err(error),
        None => Ok(()),
    }
}

fn remember_first_error(first_error: &mut Option<NekoError>, result: crate::error::Result<()>) {
    if let Err(error) = result
        && first_error.is_none()
    {
        *first_error = Some(error);
    }
}

#[cfg(test)]
mod tests {
    use std::{
        cell::RefCell,
        sync::atomic::{AtomicBool, Ordering},
    };

    use super::*;
    use crate::domain::{ProfileAuthenticationInput, SshProfileInput, TerminalPreferences};

    fn profile_input() -> SshProfileInput {
        SshProfileInput {
            label: "expiring profile".to_owned(),
            host: "example.test".to_owned(),
            port: 22,
            username: "root".to_owned(),
            authentication: ProfileAuthenticationInput::Password {
                password: "memory-only".to_owned(),
            },
            tags: Vec::new(),
            terminal: TerminalPreferences::default(),
        }
    }

    #[test]
    fn expired_reaper_claims_profile_and_retires_its_resources() {
        let owner = Uuid::new_v4();
        let store = Arc::new(EphemeralConnectionStore::new());
        let profile = store.create(owner, profile_input()).expect("profile");
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        let transfer = Arc::new(SftpTransferService::new(Arc::clone(&ssh)));
        let terminal = Arc::new(TerminalService::new(Arc::clone(&ssh)));
        let admission = Arc::new(OperationAdmission::new());
        store.expire_for_test(profile.id);

        reap_expired_connections(&store, &ssh, &transfer, &terminal, &admission);

        assert!(matches!(
            store.browser_connection(owner, profile.id),
            Err(NekoError::ProfileNotFound)
        ));
        assert!(
            store
                .expired_connection_ids()
                .expect("expired ids")
                .is_empty()
        );
        assert!(admission.try_acquire(profile.id).is_ok());
    }

    #[test]
    fn queued_admitted_operation_does_not_start_after_close_gate() {
        let admission = OperationAdmission::new();
        let profile_id = Uuid::new_v4();
        let permit = admission.try_acquire(profile_id).expect("permit");
        let close_guard = admission.begin_close(profile_id).expect("close guard");
        let ran = AtomicBool::new(false);

        let result = execute_admitted_operation(&permit, |_| {
            ran.store(true, Ordering::Release);
            Ok(())
        });

        assert!(matches!(result, Err(NekoError::ProfileNotFound)));
        assert!(!ran.load(Ordering::Acquire));
        drop(permit);
        close_guard.retire();
    }

    #[test]
    fn synchronous_activity_permit_spans_the_queue_write() {
        let admission = OperationAdmission::new();
        let profile_id = Uuid::new_v4();
        let permit = admission
            .try_acquire_activity(profile_id)
            .expect("activity permit");
        let close_guard = RefCell::new(None);
        let enqueued = AtomicBool::new(false);

        let result = execute_admitted_operation(&permit, |_| {
            // Model a terminal queue write that linearized before disconnect. The close guard must
            // still see this synchronous activity as active until the command returns.
            enqueued.store(true, Ordering::Release);
            let guard = admission.begin_close(profile_id).expect("close guard");
            assert!(!guard.wait_for_idle(Duration::from_millis(1)));
            close_guard.replace(Some(guard));
            Ok(())
        });

        assert!(enqueued.load(Ordering::Acquire));
        assert!(matches!(result, Err(NekoError::ProfileNotFound)));
        let guard = close_guard.into_inner().expect("stored close guard");
        drop(permit);
        assert!(guard.wait_for_idle(Duration::from_secs(1)));
        guard.retire();
    }
}
