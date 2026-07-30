//! Bounded, renderer-safe SFTP upload and download jobs.
//!
//! The webview supplies upload bytes rather than a local filesystem path.  This deliberately
//! prevents a compromised renderer from asking Rust to read arbitrary files.  Downloads are
//! emitted as small base64 chunks, so the webview can use the browser/Tauri save flow chosen by
//! the user; Rust never writes a renderer-selected local path either.
//!
//! Each file gets an independent verified/authenticated SFTP connection.  A small worker pool
//! makes multi-file transfers concurrent without sharing a libssh2 session across threads.

use std::{
    collections::{HashMap, HashSet, VecDeque},
    io::{Read, Write},
    path::Path,
    sync::{
        Arc, Condvar, Mutex,
        atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::{Deserialize, Serialize};
use ssh2::{ErrorCode, OpenFlags, OpenType, RenameFlags, Session, Sftp};
use tauri::{AppHandle, Emitter, Runtime, ipc::Channel as IpcChannel};
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    error::{NekoError, Result},
    operation_admission::{OperationPermit, ProfileCloseSignal},
    ssh::{SshService, validate_remote_path},
};

pub const SSH_SFTP_TRANSFER_PROGRESS_EVENT: &str = "ssh-sftp-transfer-progress";

const MAX_ACTIVE_JOBS: usize = 4;
const MAX_WORKERS_PER_JOB: usize = 4;
const MAX_FILES_PER_JOB: usize = 32;
const MAX_FILE_BYTES: usize = 16 * 1024 * 1024;
const MAX_JOB_BYTES: usize = 64 * 1024 * 1024;
const MAX_FILE_NAME_BYTES: usize = 240;
const TRANSFER_CHUNK_BYTES: usize = 64 * 1024;
const TEMPORARY_UPLOAD_MODE: i32 = 0o600;
const DOWNLOAD_ACK_TIMEOUT: Duration = Duration::from_secs(15);
const SSH_FX_NO_SUCH_FILE: i32 = 2;
const SSH_FX_OK: u32 = 0;
const SSH_FX_OP_UNSUPPORTED: u32 = 8;
const SSH_FXP_INIT: u8 = 1;
const SSH_FXP_VERSION: u8 = 2;
const SSH_FXP_STATUS: u8 = 101;
const SSH_FXP_EXTENDED: u8 = 200;
const SFTP_PROTOCOL_VERSION: u32 = 3;
const SFTP_POSIX_RENAME_EXTENSION: &[u8] = b"posix-rename@openssh.com";
const SFTP_POSIX_RENAME_VERSION: &[u8] = b"1";
const MAX_SFTP_CONTROL_PACKET_BYTES: usize = 256 * 1024;

/// Narrow upload input: the webview may provide only a simple remote basename and file bytes.
/// It cannot ask the Rust process to read a local path.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpUploadInput {
    pub name: String,
    pub data_base64: String,
    /// False by default.  Replacing an existing remote filename is destructive and must be an
    /// explicit UI-confirmed action.
    #[serde(default)]
    pub overwrite: bool,
}

/// Narrow download input: `save_as` is a filename for the webview's user-mediated save flow.
/// It is never interpreted as a local Rust filesystem path.
#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpDownloadInput {
    pub remote_path: String,
    pub save_as: String,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SshSftpTransferDirection {
    Upload,
    Download,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SshSftpTransferStatus {
    Completed,
    CompletedWithErrors,
    Cancelled,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpTransferItem {
    pub file_id: Uuid,
    pub name: String,
    pub size: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpTransferStarted {
    pub job_id: Uuid,
    pub direction: SshSftpTransferDirection,
    pub total_files: usize,
    pub total_bytes: Option<u64>,
    pub items: Vec<SshSftpTransferItem>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpTransferProgress {
    pub job_id: Uuid,
    pub file_id: Uuid,
    pub name: String,
    pub direction: SshSftpTransferDirection,
    pub file_bytes_transferred: u64,
    pub file_total_bytes: Option<u64>,
    pub job_bytes_transferred: u64,
    pub job_total_bytes: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpDownloadChunk {
    pub job_id: Uuid,
    pub file_id: Uuid,
    pub name: String,
    /// Monotonic per-job acknowledgement token.  It is not a file offset because a job may
    /// download multiple files.
    pub sequence: u64,
    pub offset: u64,
    /// Standard base64 keeps binary transfer data JSON-safe for Tauri events.  Chunks are capped
    /// at 64 KiB before encoding.
    pub data_base64: String,
}

/// Download bytes and completion are sent only through the caller-provided IPC channel.  This
/// attaches the receiver before the command starts, preventing a fast SFTP worker from racing
/// a global-event subscription or the command response.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(
    tag = "kind",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum SshSftpDownloadEvent {
    Started(SshSftpTransferStarted),
    Chunk(SshSftpDownloadChunk),
    Completed(SshSftpTransferCompleted),
}

/// Upload progress is scoped to the IPC channel created by the caller before invoking start.
/// Fast files can therefore report completion before the command promise resolves without racing
/// a global event listener.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(
    tag = "kind",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum SshSftpUploadEvent {
    Started(SshSftpTransferStarted),
    Progress(SshSftpTransferProgress),
    Completed(SshSftpTransferCompleted),
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpTransferCompleted {
    pub job_id: Uuid,
    pub direction: SshSftpTransferDirection,
    pub status: SshSftpTransferStatus,
    pub completed_files: usize,
    pub failed_files: usize,
    pub cancelled_files: usize,
    pub bytes_transferred: u64,
    pub total_bytes: Option<u64>,
}

pub struct SftpTransferService {
    ssh: Arc<SshService>,
    registry: Arc<Mutex<TransferRegistry>>,
    accepting_starts: AtomicBool,
}

#[derive(Default)]
struct TransferRegistry {
    jobs: HashMap<Uuid, Arc<JobControl>>,
    profiles: HashMap<Uuid, Uuid>,
}

struct JobControl {
    cancelled: AtomicBool,
    /// Keeps this background job represented in the profile/global admission counts until every
    /// worker and registry reference has gone away. Disconnect's five-second drain therefore
    /// observes jobs that outlive the command which started them.
    operation_permit: OperationPermit,
    download_flow: DownloadOutputFlow,
}

impl JobControl {
    fn is_cancelled(&self) -> bool {
        self.cancelled.load(Ordering::Acquire) || self.operation_permit.profile_is_closing()
    }

    fn remote_effect_allowed(&self) -> bool {
        !self.cancelled.load(Ordering::SeqCst)
            && self
                .operation_permit
                .close_signal()
                .try_begin_remote_effect()
    }
}

struct DownloadOutputFlow {
    state: Mutex<DownloadAckState>,
    ready: Condvar,
}

struct DownloadAckState {
    next_sequence: u64,
    pending_sequence: Option<u64>,
}

impl DownloadOutputFlow {
    const fn new() -> Self {
        Self {
            state: Mutex::new(DownloadAckState {
                next_sequence: 1,
                pending_sequence: None,
            }),
            ready: Condvar::new(),
        }
    }

    /// Sends at most one 64 KiB SFTP chunk until the renderer has acknowledged it.  A download
    /// job intentionally uses one worker, so this cap is strict even if a hostile renderer stops
    /// reading its Tauri IPC channel.
    fn send_and_wait(
        &self,
        events: &IpcChannel<SshSftpDownloadEvent>,
        chunk: SshSftpDownloadChunk,
        cancelled: &AtomicBool,
    ) -> std::result::Result<(), &'static str> {
        let sequence = {
            let mut state = self
                .state
                .lock()
                .map_err(|_| "ssh_sftp_download_channel_failed")?;
            if state.pending_sequence.is_some() {
                return Err("ssh_sftp_download_channel_failed");
            }
            let sequence = state.next_sequence;
            state.next_sequence = state
                .next_sequence
                .checked_add(1)
                .ok_or("ssh_sftp_download_channel_failed")?;
            state.pending_sequence = Some(sequence);
            sequence
        };

        if events
            .send(SshSftpDownloadEvent::Chunk(SshSftpDownloadChunk {
                sequence,
                ..chunk
            }))
            .is_err()
        {
            self.clear(sequence);
            return Err("ssh_sftp_download_channel_failed");
        }

        let deadline = Instant::now() + DOWNLOAD_ACK_TIMEOUT;
        let mut state = self
            .state
            .lock()
            .map_err(|_| "ssh_sftp_download_channel_failed")?;
        while state.pending_sequence == Some(sequence) {
            if cancelled.load(Ordering::Acquire) {
                state.pending_sequence = None;
                self.ready.notify_all();
                return Err("ssh_sftp_download_cancelled");
            }
            let Some(remaining) = deadline.checked_duration_since(Instant::now()) else {
                state.pending_sequence = None;
                self.ready.notify_all();
                return Err("ssh_sftp_download_ack_timeout");
            };
            let (next_state, timeout) = self
                .ready
                .wait_timeout(state, remaining)
                .map_err(|_| "ssh_sftp_download_channel_failed")?;
            state = next_state;
            if timeout.timed_out() && state.pending_sequence == Some(sequence) {
                state.pending_sequence = None;
                self.ready.notify_all();
                return Err("ssh_sftp_download_ack_timeout");
            }
        }
        Ok(())
    }

    fn acknowledge(&self, sequence: u64) {
        if let Ok(mut state) = self.state.lock()
            && state.pending_sequence == Some(sequence)
        {
            state.pending_sequence = None;
            self.ready.notify_all();
        }
    }

    fn cancel(&self) {
        if let Ok(mut state) = self.state.lock() {
            state.pending_sequence = None;
            self.ready.notify_all();
        }
    }

    fn clear(&self, sequence: u64) {
        if let Ok(mut state) = self.state.lock()
            && state.pending_sequence == Some(sequence)
        {
            state.pending_sequence = None;
            self.ready.notify_all();
        }
    }
}

struct JobTracker {
    bytes_transferred: AtomicU64,
    completed_files: AtomicUsize,
    failed_files: AtomicUsize,
    cancelled_files: AtomicUsize,
    total_bytes: Option<u64>,
}

enum TransferTask {
    Upload(UploadTask),
    Download(DownloadTask),
}

struct UploadTask {
    file_id: Uuid,
    name: String,
    remote_path: String,
    overwrite: bool,
    bytes: Arc<Zeroizing<Vec<u8>>>,
}

struct DownloadTask {
    file_id: Uuid,
    name: String,
    remote_path: String,
}

enum TransferOutcome {
    Completed,
    Failed,
    Cancelled,
}

type DownloadProgressEmitter = Arc<dyn Fn(SshSftpTransferProgress) + Send + Sync>;

impl SftpTransferService {
    pub fn new(ssh: Arc<SshService>) -> Self {
        Self {
            ssh,
            registry: Arc::new(Mutex::new(TransferRegistry::default())),
            accepting_starts: AtomicBool::new(true),
        }
    }

    pub fn start_upload<R: Runtime>(
        &self,
        _app: AppHandle<R>,
        profile_id: Uuid,
        remote_directory: String,
        files: Vec<SshSftpUploadInput>,
        events: IpcChannel<SshSftpUploadEvent>,
        operation_permit: OperationPermit,
    ) -> Result<SshSftpTransferStarted> {
        self.start_upload_core(
            profile_id,
            remote_directory,
            files,
            events,
            operation_permit,
        )
    }

    #[cfg(test)]
    pub(crate) fn start_upload_headless(
        &self,
        profile_id: Uuid,
        remote_directory: String,
        files: Vec<SshSftpUploadInput>,
        events: IpcChannel<SshSftpUploadEvent>,
        operation_permit: OperationPermit,
    ) -> Result<SshSftpTransferStarted> {
        self.start_upload_core(
            profile_id,
            remote_directory,
            files,
            events,
            operation_permit,
        )
    }

    fn start_upload_core(
        &self,
        profile_id: Uuid,
        remote_directory: String,
        files: Vec<SshSftpUploadInput>,
        events: IpcChannel<SshSftpUploadEvent>,
        operation_permit: OperationPermit,
    ) -> Result<SshSftpTransferStarted> {
        validate_remote_path(&remote_directory)?;
        let job_id = Uuid::new_v4();
        let control = self.reserve_job(job_id, profile_id, operation_permit)?;
        // `begin_close` can race between the command's initial check and registry insertion. The
        // durable profile signal closes that gap even if disconnect's first registry scan missed
        // this just-created job.
        if control.is_cancelled() {
            self.remove_job(job_id);
            return Err(NekoError::ProfileNotFound);
        }

        let prepared = (|| {
            validate_file_count(files.len())?;
            let mut total_bytes = 0_usize;
            let mut items = Vec::with_capacity(files.len());
            let mut tasks = Vec::with_capacity(files.len());
            let mut destinations = HashSet::with_capacity(files.len());

            for input in files {
                let name = validate_file_name(&input.name)?.to_owned();
                let encoded = Zeroizing::new(input.data_base64);
                if encoded.len() > max_encoded_file_bytes() {
                    return Err(NekoError::SshTransferFileTooLarge);
                }
                let bytes = STANDARD
                    .decode(encoded.as_bytes())
                    .map_err(|_| NekoError::SshTransferInputInvalid)?;
                if bytes.len() > MAX_FILE_BYTES {
                    return Err(NekoError::SshTransferFileTooLarge);
                }
                total_bytes = total_bytes
                    .checked_add(bytes.len())
                    .ok_or(NekoError::SshTransferTotalTooLarge)?;
                if total_bytes > MAX_JOB_BYTES {
                    return Err(NekoError::SshTransferTotalTooLarge);
                }

                let file_id = Uuid::new_v4();
                let remote_path = join_remote_path(&remote_directory, &name)?;
                // Two workers targeting the same final pathname would make overwrite behavior
                // nondeterministic, even with temporary upload names.  Reject it up front.
                if !destinations.insert(remote_path.clone()) {
                    return Err(NekoError::SshTransferInputInvalid);
                }
                items.push(SshSftpTransferItem {
                    file_id,
                    name: name.clone(),
                    size: Some(bytes.len() as u64),
                });
                tasks.push(TransferTask::Upload(UploadTask {
                    file_id,
                    name,
                    remote_path,
                    overwrite: input.overwrite,
                    bytes: Arc::new(Zeroizing::new(bytes)),
                }));
            }

            Ok::<_, NekoError>((items, tasks, total_bytes as u64))
        })();

        let (items, tasks, total_bytes) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.remove_job(job_id);
                return Err(error);
            }
        };
        if control.is_cancelled() {
            self.remove_job(job_id);
            return Err(NekoError::ProfileNotFound);
        }

        let started = SshSftpTransferStarted {
            job_id,
            direction: SshSftpTransferDirection::Upload,
            total_files: items.len(),
            total_bytes: Some(total_bytes),
            items,
        };
        if events
            .send(SshSftpUploadEvent::Started(started.clone()))
            .is_err()
        {
            self.remove_job(job_id);
            return Err(NekoError::SshTransferFailed);
        }

        let tracker = Arc::new(JobTracker::new(Some(total_bytes)));
        if let Err(error) = self.spawn_job(
            profile_id,
            job_id,
            SshSftpTransferDirection::Upload,
            tasks,
            control,
            tracker,
            Some(events),
            None,
            Arc::new(|_| {}),
        ) {
            self.remove_job(job_id);
            return Err(error);
        }

        Ok(started)
    }

    pub fn start_download<R: Runtime>(
        &self,
        app: AppHandle<R>,
        profile_id: Uuid,
        files: Vec<SshSftpDownloadInput>,
        events: IpcChannel<SshSftpDownloadEvent>,
        operation_permit: OperationPermit,
    ) -> Result<SshSftpTransferStarted> {
        self.start_download_core(
            profile_id,
            files,
            events,
            operation_permit,
            Arc::new(move |progress| {
                emit(&app, SSH_SFTP_TRANSFER_PROGRESS_EVENT, progress);
            }),
        )
    }

    #[cfg(test)]
    pub(crate) fn start_download_headless(
        &self,
        profile_id: Uuid,
        files: Vec<SshSftpDownloadInput>,
        events: IpcChannel<SshSftpDownloadEvent>,
        operation_permit: OperationPermit,
    ) -> Result<SshSftpTransferStarted> {
        self.start_download_core(
            profile_id,
            files,
            events,
            operation_permit,
            Arc::new(|_| {}),
        )
    }

    fn start_download_core(
        &self,
        profile_id: Uuid,
        files: Vec<SshSftpDownloadInput>,
        events: IpcChannel<SshSftpDownloadEvent>,
        operation_permit: OperationPermit,
        progress_emitter: DownloadProgressEmitter,
    ) -> Result<SshSftpTransferStarted> {
        let job_id = Uuid::new_v4();
        let control = self.reserve_job(job_id, profile_id, operation_permit)?;
        if control.is_cancelled() {
            self.remove_job(job_id);
            return Err(NekoError::ProfileNotFound);
        }
        let prepared = (|| {
            validate_file_count(files.len())?;
            let mut items = Vec::with_capacity(files.len());
            let mut tasks = Vec::with_capacity(files.len());
            let mut save_names = HashSet::with_capacity(files.len());
            for input in files {
                validate_remote_path(&input.remote_path)?;
                let name = validate_file_name(&input.save_as)?.to_owned();
                // The browser save flow needs an unambiguous suggested name per download item.
                if !save_names.insert(name.clone()) {
                    return Err(NekoError::SshTransferInputInvalid);
                }
                let file_id = Uuid::new_v4();
                items.push(SshSftpTransferItem {
                    file_id,
                    name: name.clone(),
                    size: None,
                });
                tasks.push(TransferTask::Download(DownloadTask {
                    file_id,
                    name,
                    remote_path: input.remote_path,
                }));
            }
            Ok::<_, NekoError>((items, tasks))
        })();

        let (items, tasks) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.remove_job(job_id);
                return Err(error);
            }
        };
        if control.is_cancelled() {
            self.remove_job(job_id);
            return Err(NekoError::ProfileNotFound);
        }

        let started = SshSftpTransferStarted {
            job_id,
            direction: SshSftpTransferDirection::Download,
            total_files: items.len(),
            total_bytes: None,
            items,
        };
        if events
            .send(SshSftpDownloadEvent::Started(started.clone()))
            .is_err()
        {
            self.remove_job(job_id);
            return Err(NekoError::SshTransferFailed);
        }

        let tracker = Arc::new(JobTracker::new(None));
        if let Err(error) = self.spawn_job(
            profile_id,
            job_id,
            SshSftpTransferDirection::Download,
            tasks,
            control,
            tracker,
            None,
            Some(events),
            progress_emitter,
        ) {
            self.remove_job(job_id);
            return Err(error);
        }

        Ok(started)
    }

    pub fn cancel(&self, job_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        let control = registry
            .jobs
            .get(&job_id)
            .ok_or(NekoError::SshTransferJobNotFound)?;
        control.cancelled.store(true, Ordering::Release);
        control.download_flow.cancel();
        Ok(())
    }

    /// Releases a chunk only after the renderer has copied it into its verified collector.
    pub fn acknowledge_download(&self, job_id: Uuid, sequence: u64) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        let control = registry
            .jobs
            .get(&job_id)
            .ok_or(NekoError::SshTransferJobNotFound)?;
        control.download_flow.acknowledge(sequence);
        Ok(())
    }

    /// Resolves a running transfer to its ephemeral profile so renderer acknowledgements can
    /// count as real activity without exposing credentials or the worker's SSH session.
    pub fn profile_id_for_job(&self, job_id: Uuid) -> Result<Uuid> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        registry
            .profiles
            .get(&job_id)
            .copied()
            .ok_or(NekoError::SshTransferJobNotFound)
    }

    /// Cancels every transfer that belongs to one ephemeral SSH profile. Workers observe the
    /// cancellation flag between bounded chunks and release their SSH sessions asynchronously.
    pub fn cancel_for_profile(&self, profile_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        for (job_id, control) in &registry.jobs {
            if registry.profiles.get(job_id) == Some(&profile_id) {
                control.cancelled.store(true, Ordering::Release);
                control.download_flow.cancel();
            }
        }
        Ok(())
    }

    /// Creates one remote directory.  Recursive parent creation is intentionally not exposed:
    /// callers must present the exact destination and handle any missing parent explicitly.
    pub fn create_directory(
        &self,
        profile_id: Uuid,
        path: String,
        close_signal: ProfileCloseSignal,
    ) -> Result<()> {
        validate_remote_path(&path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| NekoError::SshTransferFailed)?;
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        sftp.mkdir(Path::new(&path), 0o755)
            .map_err(|_| NekoError::SshTransferFailed)
    }

    /// Renames or moves one remote filesystem entry after a fresh host-key/authentication check.
    /// Replacing the destination is allowed only when the caller passes an explicit confirmation.
    pub fn rename_remote(
        &self,
        profile_id: Uuid,
        from_path: String,
        to_path: String,
        overwrite: bool,
        close_signal: ProfileCloseSignal,
    ) -> Result<()> {
        validate_destructive_remote_path(&from_path)?;
        validate_destructive_remote_path(&to_path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        if overwrite {
            if !close_signal.try_begin_remote_effect() {
                return Err(NekoError::ProfileNotFound);
            }
            atomic_overwrite_rename(&session, &from_path, &to_path)
        } else {
            let sftp = session.sftp().map_err(|_| NekoError::SshTransferFailed)?;
            if sftp_path_exists(&sftp, Path::new(&to_path))? {
                return Err(NekoError::SshTransferFailed);
            }
            if !close_signal.try_begin_remote_effect() {
                return Err(NekoError::ProfileNotFound);
            }
            sftp.rename(
                Path::new(&from_path),
                Path::new(&to_path),
                Some(non_overwriting_rename_flags()),
            )
            .map_err(|_| NekoError::SshTransferFailed)
        }
    }

    /// Deletes one file/symlink or an empty directory only.  Recursive deletion is deliberately
    /// unavailable on the IPC surface, preventing a renderer from issuing `rm -rf` equivalents.
    pub fn delete_remote(
        &self,
        profile_id: Uuid,
        path: String,
        close_signal: ProfileCloseSignal,
    ) -> Result<()> {
        validate_destructive_remote_path(&path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| NekoError::SshTransferFailed)?;
        let stat = sftp
            .lstat(Path::new(&path))
            .map_err(|_| NekoError::SshTransferFailed)?;
        // `lstat` is read-only and may block. Recheck after it and immediately before the actual
        // unlink/rmdir so disconnect wins whenever the destructive call has not begun yet.
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        if stat.is_dir() {
            sftp.rmdir(Path::new(&path))
                .map_err(|_| NekoError::SshTransferFailed)
        } else {
            sftp.unlink(Path::new(&path))
                .map_err(|_| NekoError::SshTransferFailed)
        }
    }

    /// Called before application shutdown. Workers check the flag between chunks and TCP streams
    /// retain the existing bounded SSH operation timeout for any in-flight network call.
    pub fn cancel_all(&self) -> Result<()> {
        self.accepting_starts.store(false, Ordering::Release);
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        for control in registry.jobs.values() {
            control.cancelled.store(true, Ordering::Release);
            control.download_flow.cancel();
        }
        Ok(())
    }

    fn reserve_job(
        &self,
        job_id: Uuid,
        profile_id: Uuid,
        operation_permit: OperationPermit,
    ) -> Result<Arc<JobControl>> {
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(NekoError::Locked);
        }
        let mut registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(NekoError::Locked);
        }
        if registry.jobs.len() >= MAX_ACTIVE_JOBS {
            return Err(NekoError::SshTransferJobLimit);
        }
        let control = Arc::new(JobControl {
            cancelled: AtomicBool::new(false),
            operation_permit,
            download_flow: DownloadOutputFlow::new(),
        });
        registry.jobs.insert(job_id, Arc::clone(&control));
        registry.profiles.insert(job_id, profile_id);
        Ok(control)
    }

    fn remove_job(&self, job_id: Uuid) {
        if let Ok(mut registry) = self.registry.lock() {
            registry.jobs.remove(&job_id);
            registry.profiles.remove(&job_id);
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn spawn_job(
        &self,
        profile_id: Uuid,
        job_id: Uuid,
        direction: SshSftpTransferDirection,
        tasks: Vec<TransferTask>,
        control: Arc<JobControl>,
        tracker: Arc<JobTracker>,
        upload_events: Option<IpcChannel<SshSftpUploadEvent>>,
        download_events: Option<IpcChannel<SshSftpDownloadEvent>>,
        progress_emitter: DownloadProgressEmitter,
    ) -> Result<()> {
        // Downloads use one worker: the sole outstanding 64 KiB chunk is therefore a strict
        // end-to-end cap on queued WebView data. Uploads retain their bounded parallelism.
        let worker_count = if direction == SshSftpTransferDirection::Download {
            1
        } else {
            tasks.len().min(MAX_WORKERS_PER_JOB)
        };
        let queue = Arc::new(Mutex::new(VecDeque::from(tasks)));
        let ssh = Arc::clone(&self.ssh);
        let registry = Arc::clone(&self.registry);
        let upload_events = upload_events.map(Arc::new);
        let download_events = download_events.map(Arc::new);
        thread::Builder::new()
            .name(format!("nekossh-sftp-job-{job_id}"))
            .spawn(move || {
                // One supervisor owns a small scoped worker pool.  Every worker gets a separate
                // SSH session, so libssh2 sessions are never accessed from more than one thread.
                thread::scope(|scope| {
                    for _ in 0..worker_count {
                        let worker_ssh = Arc::clone(&ssh);
                        let worker_queue = Arc::clone(&queue);
                        let worker_control = Arc::clone(&control);
                        let worker_tracker = Arc::clone(&tracker);
                        let worker_upload_events = upload_events.as_ref().map(Arc::clone);
                        let worker_download_events = download_events.as_ref().map(Arc::clone);
                        let worker_progress_emitter = Arc::clone(&progress_emitter);
                        scope.spawn(move || {
                            transfer_worker(
                                worker_ssh,
                                profile_id,
                                job_id,
                                worker_queue,
                                worker_control,
                                worker_tracker,
                                worker_upload_events,
                                worker_download_events,
                                worker_progress_emitter,
                            );
                        });
                    }
                });
                finish_job(
                    job_id,
                    direction,
                    &tracker,
                    &registry,
                    upload_events.as_deref(),
                    download_events.as_deref(),
                );
            })
            .map_err(|_| NekoError::SshTransferFailed)?;
        Ok(())
    }
}

impl JobTracker {
    fn new(total_bytes: Option<u64>) -> Self {
        Self {
            bytes_transferred: AtomicU64::new(0),
            completed_files: AtomicUsize::new(0),
            failed_files: AtomicUsize::new(0),
            cancelled_files: AtomicUsize::new(0),
            total_bytes,
        }
    }

    fn add_known_bytes(&self, count: u64) -> u64 {
        self.bytes_transferred.fetch_add(count, Ordering::AcqRel) + count
    }

    /// Atomically reserves received bytes so concurrent downloads cannot exceed the job cap.
    fn reserve_download_bytes(&self, count: u64) -> Result<u64> {
        let mut current = self.bytes_transferred.load(Ordering::Acquire);
        loop {
            let next = current
                .checked_add(count)
                .ok_or(NekoError::SshTransferTotalTooLarge)?;
            if next > MAX_JOB_BYTES as u64 {
                return Err(NekoError::SshTransferTotalTooLarge);
            }
            match self.bytes_transferred.compare_exchange_weak(
                current,
                next,
                Ordering::AcqRel,
                Ordering::Acquire,
            ) {
                Ok(_) => return Ok(next),
                Err(observed) => current = observed,
            }
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn transfer_worker(
    ssh: Arc<SshService>,
    profile_id: Uuid,
    job_id: Uuid,
    queue: Arc<Mutex<VecDeque<TransferTask>>>,
    control: Arc<JobControl>,
    tracker: Arc<JobTracker>,
    upload_events: Option<Arc<IpcChannel<SshSftpUploadEvent>>>,
    download_events: Option<Arc<IpcChannel<SshSftpDownloadEvent>>>,
    progress_emitter: DownloadProgressEmitter,
) {
    loop {
        let task = match queue.lock() {
            Ok(mut tasks) => tasks.pop_front(),
            Err(_) => {
                control.cancelled.store(true, Ordering::Release);
                return;
            }
        };
        let Some(task) = task else {
            return;
        };

        let outcome = if control.is_cancelled() {
            TransferOutcome::Cancelled
        } else {
            match task {
                TransferTask::Upload(task) => upload_one(
                    &ssh,
                    profile_id,
                    job_id,
                    &task,
                    &control,
                    &tracker,
                    upload_events.as_deref(),
                ),
                TransferTask::Download(task) => download_one(
                    &ssh,
                    profile_id,
                    job_id,
                    &task,
                    &control,
                    &tracker,
                    download_events.as_deref(),
                    &progress_emitter,
                ),
            }
        };
        report_file_outcome(outcome, &tracker);
    }
}

fn upload_one(
    ssh: &SshService,
    profile_id: Uuid,
    job_id: Uuid,
    task: &UploadTask,
    control: &JobControl,
    tracker: &JobTracker,
    events: Option<&IpcChannel<SshSftpUploadEvent>>,
) -> TransferOutcome {
    let result = (|| -> Result<bool> {
        let session = ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| NekoError::SshTransferFailed)?;
        let temporary_path = temporary_upload_path(&task.remote_path, task.file_id)?;
        if !task.overwrite && sftp_path_exists(&sftp, Path::new(&task.remote_path))? {
            return Err(NekoError::SshTransferFailed);
        }
        if !control.remote_effect_allowed() {
            return Ok(false);
        }
        let mut file = sftp
            .open_mode(
                Path::new(&temporary_path),
                temporary_upload_open_flags(),
                TEMPORARY_UPLOAD_MODE,
                OpenType::File,
            )
            .map_err(|_| NekoError::SshTransferFailed)?;
        let mut offset = 0_usize;
        let result = (|| -> Result<bool> {
            while offset < task.bytes.len() {
                if !control.remote_effect_allowed() {
                    return Ok(false);
                }
                let end = (offset + TRANSFER_CHUNK_BYTES).min(task.bytes.len());
                let written = file
                    .write(&task.bytes.as_ref()[offset..end])
                    .map_err(|_| NekoError::SshTransferFailed)?;
                if written == 0 {
                    return Err(NekoError::SshTransferFailed);
                }
                offset += written;
                let job_bytes = tracker.add_known_bytes(written as u64);
                let events = events.ok_or(NekoError::SshTransferFailed)?;
                if events
                    .send(SshSftpUploadEvent::Progress(SshSftpTransferProgress {
                        job_id,
                        file_id: task.file_id,
                        name: task.name.clone(),
                        direction: SshSftpTransferDirection::Upload,
                        file_bytes_transferred: offset as u64,
                        file_total_bytes: Some(task.bytes.len() as u64),
                        job_bytes_transferred: job_bytes,
                        job_total_bytes: tracker.total_bytes,
                    }))
                    .is_err()
                {
                    control.cancelled.store(true, Ordering::Release);
                    return Ok(false);
                }
            }
            if !control.remote_effect_allowed() {
                return Ok(false);
            }
            file.flush().map_err(|_| NekoError::SshTransferFailed)?;
            if !control.remote_effect_allowed() {
                return Ok(false);
            }
            // Best effort: older SFTP servers may lack the OpenSSH fsync extension, but a
            // successful close/rename is still a valid completed upload.
            let _ = file.fsync();
            if !control.remote_effect_allowed() {
                return Ok(false);
            }
            Ok(true)
        })();
        drop(file);
        match result {
            Ok(true) => {
                // Closing the remote handle can itself block. Disconnect may win while `drop`
                // waits, so linearize again immediately before publishing the completed file.
                if !control.remote_effect_allowed() {
                    let _ = sftp.unlink(Path::new(&temporary_path));
                    return Ok(false);
                }
                let publish_result = if task.overwrite {
                    atomic_overwrite_rename(&session, &temporary_path, &task.remote_path)
                } else {
                    sftp.rename(
                        Path::new(&temporary_path),
                        Path::new(&task.remote_path),
                        Some(non_overwriting_rename_flags()),
                    )
                    .map_err(|_| NekoError::SshTransferFailed)
                };
                if let Err(error) = publish_result {
                    let _ = sftp.unlink(Path::new(&temporary_path));
                    return Err(error);
                }
                Ok(true)
            }
            Ok(false) => {
                let _ = sftp.unlink(Path::new(&temporary_path));
                Ok(false)
            }
            Err(error) => {
                let _ = sftp.unlink(Path::new(&temporary_path));
                Err(error)
            }
        }
    })();
    outcome_from_result(result, control)
}

#[allow(clippy::too_many_arguments)]
fn download_one(
    ssh: &SshService,
    profile_id: Uuid,
    job_id: Uuid,
    task: &DownloadTask,
    control: &JobControl,
    tracker: &JobTracker,
    events: Option<&IpcChannel<SshSftpDownloadEvent>>,
    progress_emitter: &DownloadProgressEmitter,
) -> TransferOutcome {
    let result = (|| -> Result<bool> {
        let session = ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| NekoError::SshTransferFailed)?;
        if control.is_cancelled() {
            return Ok(false);
        }
        let stat = sftp
            .stat(Path::new(&task.remote_path))
            .map_err(|_| NekoError::SshTransferFailed)?;
        if stat.is_dir() || stat.size.is_some_and(|size| size > MAX_FILE_BYTES as u64) {
            return Err(NekoError::SshTransferFileTooLarge);
        }
        let file_total_bytes = stat.size;
        let mut file = sftp
            .open(Path::new(&task.remote_path))
            .map_err(|_| NekoError::SshTransferFailed)?;
        let mut buffer = [0_u8; TRANSFER_CHUNK_BYTES];
        let mut offset = 0_u64;
        loop {
            if control.is_cancelled() {
                return Ok(false);
            }
            let read = file
                .read(&mut buffer)
                .map_err(|_| NekoError::SshTransferFailed)?;
            if read == 0 {
                return Ok(true);
            }
            let next_offset = offset
                .checked_add(read as u64)
                .ok_or(NekoError::SshTransferFileTooLarge)?;
            if next_offset > MAX_FILE_BYTES as u64 {
                return Err(NekoError::SshTransferFileTooLarge);
            }
            let job_bytes = tracker.reserve_download_bytes(read as u64)?;
            let events = events.ok_or(NekoError::SshTransferFailed)?;
            if control
                .download_flow
                .send_and_wait(
                    events,
                    SshSftpDownloadChunk {
                        job_id,
                        file_id: task.file_id,
                        name: task.name.clone(),
                        sequence: 0,
                        offset,
                        data_base64: STANDARD.encode(&buffer[..read]),
                    },
                    &control.cancelled,
                )
                .is_err()
            {
                // A disconnected renderer or missing acknowledgement must stop the whole job;
                // otherwise subsequent chunks could accumulate in a dead WebView queue.
                control.cancelled.store(true, Ordering::Release);
                return Ok(false);
            }
            offset = next_offset;
            progress_emitter(SshSftpTransferProgress {
                job_id,
                file_id: task.file_id,
                name: task.name.clone(),
                direction: SshSftpTransferDirection::Download,
                file_bytes_transferred: offset,
                file_total_bytes,
                job_bytes_transferred: job_bytes,
                job_total_bytes: tracker.total_bytes,
            });
        }
    })();
    outcome_from_result(result, control)
}

fn outcome_from_result(result: Result<bool>, control: &JobControl) -> TransferOutcome {
    match result {
        Ok(true) => TransferOutcome::Completed,
        Ok(false) => TransferOutcome::Cancelled,
        Err(error) => {
            if is_job_fatal(&error) {
                control.cancelled.store(true, Ordering::Release);
            }
            if control.is_cancelled() && matches!(error, NekoError::Locked) {
                TransferOutcome::Cancelled
            } else {
                TransferOutcome::Failed
            }
        }
    }
}

fn is_job_fatal(error: &NekoError) -> bool {
    matches!(
        error,
        NekoError::Locked
            | NekoError::SshHostKeyUntrusted
            | NekoError::SshHostKeyMismatch
            | NekoError::SshAuthenticationFailed
            | NekoError::SshTransferTotalTooLarge
    )
}

fn report_file_outcome(outcome: TransferOutcome, tracker: &JobTracker) {
    match outcome {
        TransferOutcome::Completed => {
            tracker.completed_files.fetch_add(1, Ordering::AcqRel);
        }
        TransferOutcome::Failed => {
            tracker.failed_files.fetch_add(1, Ordering::AcqRel);
        }
        TransferOutcome::Cancelled => {
            tracker.cancelled_files.fetch_add(1, Ordering::AcqRel);
        }
    }
}

fn finish_job(
    job_id: Uuid,
    direction: SshSftpTransferDirection,
    tracker: &JobTracker,
    registry: &Arc<Mutex<TransferRegistry>>,
    upload_events: Option<&IpcChannel<SshSftpUploadEvent>>,
    download_events: Option<&IpcChannel<SshSftpDownloadEvent>>,
) {
    let completed_files = tracker.completed_files.load(Ordering::Acquire);
    let failed_files = tracker.failed_files.load(Ordering::Acquire);
    let cancelled_files = tracker.cancelled_files.load(Ordering::Acquire);
    let status = transfer_status(failed_files, cancelled_files);
    if let Ok(mut active) = registry.lock() {
        active.jobs.remove(&job_id);
        active.profiles.remove(&job_id);
    }
    let completed = SshSftpTransferCompleted {
        job_id,
        direction,
        status,
        completed_files,
        failed_files,
        cancelled_files,
        bytes_transferred: tracker.bytes_transferred.load(Ordering::Acquire),
        total_bytes: tracker.total_bytes,
    };
    if let Some(events) = upload_events {
        let _ = events.send(SshSftpUploadEvent::Completed(completed.clone()));
    }
    if let Some(events) = download_events {
        let _ = events.send(SshSftpDownloadEvent::Completed(completed));
    }
}

fn transfer_status(failed_files: usize, cancelled_files: usize) -> SshSftpTransferStatus {
    if failed_files > 0 {
        SshSftpTransferStatus::CompletedWithErrors
    } else if cancelled_files > 0 {
        SshSftpTransferStatus::Cancelled
    } else {
        SshSftpTransferStatus::Completed
    }
}

fn validate_file_count(count: usize) -> Result<()> {
    if count == 0 || count > MAX_FILES_PER_JOB {
        return Err(NekoError::SshTransferInputInvalid);
    }
    Ok(())
}

fn validate_file_name(name: &str) -> Result<&str> {
    if name.is_empty()
        || name.len() > MAX_FILE_NAME_BYTES
        || name == "."
        || name == ".."
        || name.contains(['/', '\\'])
        || name.as_bytes().contains(&0)
        || name.chars().any(char::is_control)
    {
        return Err(NekoError::SshTransferInputInvalid);
    }
    Ok(name)
}

fn join_remote_path(directory: &str, file_name: &str) -> Result<String> {
    validate_remote_path(directory)?;
    validate_file_name(file_name)?;
    let path = if directory == "/" {
        format!("/{file_name}")
    } else {
        format!("{}/{}", directory.trim_end_matches('/'), file_name)
    };
    validate_remote_path(&path)?;
    Ok(path)
}

fn validate_destructive_remote_path(path: &str) -> Result<()> {
    validate_remote_path(path)?;
    // An explicit file-browser item should have a concrete name.  Do not permit the renderer to
    // request an operation on the remote root or its implicit current/parent directory.
    if path.trim_matches('/').is_empty()
        || path.split('/').any(|segment| matches!(segment, "." | ".."))
    {
        return Err(NekoError::SshTransferInputInvalid);
    }
    Ok(())
}

fn temporary_upload_path(remote_path: &str, file_id: Uuid) -> Result<String> {
    let (directory, file_name) = remote_path
        .rsplit_once('/')
        .ok_or(NekoError::SshTransferInputInvalid)?;
    let directory = if directory.is_empty() { "/" } else { directory };
    let temporary_name = format!(".{file_name}.nekossh-{file_id}.part");
    join_remote_path(directory, &temporary_name)
}

fn non_overwriting_rename_flags() -> RenameFlags {
    RenameFlags::empty()
}

fn sftp_path_exists(sftp: &Sftp, remote_path: &Path) -> Result<bool> {
    match sftp.lstat(remote_path) {
        Ok(_) => Ok(true),
        Err(error) if error.code() == ErrorCode::SFTP(SSH_FX_NO_SUCH_FILE) => Ok(false),
        // Permission denied, transport loss, and malformed/protocol errors must never be
        // interpreted as absence: doing so could turn overwrite=false into a destructive action.
        Err(_) => Err(NekoError::SshTransferFailed),
    }
}

/// Performs the OpenSSH POSIX rename extension on a separate libssh2 channel.
///
/// `ssh2::Sftp::rename` only expresses an atomic *preference* for SFTP v5 and sends an ordinary
/// SSH_FXP_RENAME on the overwhelmingly common v3 protocol.  The high-level crate does not expose
/// `libssh2_sftp_posix_rename_ex` or a safe raw SFTP handle, so reaching through its private layout
/// would be unsound. This small protocol wrapper stays on the authenticated in-process libssh2
/// session, negotiates SFTP v3 itself, requires the advertised OpenSSH extension version, and
/// fails closed if the server cannot guarantee rename(2)-style replacement.
fn atomic_overwrite_rename(session: &Session, from_path: &str, to_path: &str) -> Result<()> {
    let mut channel = session
        .channel_session()
        .map_err(|_| NekoError::SshTransferFailed)?;
    channel
        .subsystem("sftp")
        .map_err(|_| NekoError::SshTransferFailed)?;

    let operation = (|| -> std::result::Result<(), AtomicOverwriteError> {
        write_sftp_packet(
            &mut channel,
            SSH_FXP_INIT,
            &SFTP_PROTOCOL_VERSION.to_be_bytes(),
        )?;
        let (packet_type, version_payload) = read_sftp_packet(&mut channel)?;
        if packet_type != SSH_FXP_VERSION {
            return Err(AtomicOverwriteError::Protocol);
        }
        let mut version = SftpPacketCursor::new(&version_payload);
        if version.read_u32()? != SFTP_PROTOCOL_VERSION {
            return Err(AtomicOverwriteError::Protocol);
        }
        let mut supports_posix_rename = false;
        while !version.is_empty() {
            let name = version.read_string()?;
            let value = version.read_string()?;
            if name == SFTP_POSIX_RENAME_EXTENSION && value == SFTP_POSIX_RENAME_VERSION {
                supports_posix_rename = true;
            }
        }
        if !supports_posix_rename {
            return Err(AtomicOverwriteError::Unsupported);
        }

        let mut request = Vec::with_capacity(
            4 + 4 + SFTP_POSIX_RENAME_EXTENSION.len() + 4 + from_path.len() + 4 + to_path.len(),
        );
        request.extend_from_slice(&1_u32.to_be_bytes());
        push_sftp_string(&mut request, SFTP_POSIX_RENAME_EXTENSION)?;
        push_sftp_string(&mut request, from_path.as_bytes())?;
        push_sftp_string(&mut request, to_path.as_bytes())?;
        write_sftp_packet(&mut channel, SSH_FXP_EXTENDED, &request)?;

        let (packet_type, status_payload) = read_sftp_packet(&mut channel)?;
        if packet_type != SSH_FXP_STATUS {
            return Err(AtomicOverwriteError::Protocol);
        }
        let mut status = SftpPacketCursor::new(&status_payload);
        if status.read_u32()? != 1 {
            return Err(AtomicOverwriteError::Protocol);
        }
        match status.read_u32()? {
            SSH_FX_OK => Ok(()),
            SSH_FX_OP_UNSUPPORTED => Err(AtomicOverwriteError::Unsupported),
            _ => Err(AtomicOverwriteError::Remote),
        }
    })();

    // Never let channel-shutdown diagnostics replace the authoritative rename result. The SSH
    // session timeout still bounds a peer that stops responding during cleanup.
    let _ = channel.close();
    let _ = channel.wait_close();

    match operation {
        Ok(()) => Ok(()),
        Err(AtomicOverwriteError::Unsupported) => Err(NekoError::SshAtomicOverwriteUnsupported),
        Err(
            AtomicOverwriteError::Io
            | AtomicOverwriteError::Protocol
            | AtomicOverwriteError::Remote,
        ) => Err(NekoError::SshTransferFailed),
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum AtomicOverwriteError {
    Unsupported,
    Protocol,
    Remote,
    Io,
}

impl From<std::io::Error> for AtomicOverwriteError {
    fn from(_: std::io::Error) -> Self {
        Self::Io
    }
}

fn write_sftp_packet(
    channel: &mut ssh2::Channel,
    packet_type: u8,
    payload: &[u8],
) -> std::result::Result<(), AtomicOverwriteError> {
    let packet_length = payload
        .len()
        .checked_add(1)
        .filter(|length| *length <= MAX_SFTP_CONTROL_PACKET_BYTES)
        .and_then(|length| u32::try_from(length).ok())
        .ok_or(AtomicOverwriteError::Protocol)?;
    channel.write_all(&packet_length.to_be_bytes())?;
    channel.write_all(&[packet_type])?;
    channel.write_all(payload)?;
    channel.flush()?;
    Ok(())
}

fn read_sftp_packet(
    channel: &mut ssh2::Channel,
) -> std::result::Result<(u8, Vec<u8>), AtomicOverwriteError> {
    let mut length = [0_u8; 4];
    channel.read_exact(&mut length)?;
    let length =
        usize::try_from(u32::from_be_bytes(length)).map_err(|_| AtomicOverwriteError::Protocol)?;
    if !(1..=MAX_SFTP_CONTROL_PACKET_BYTES).contains(&length) {
        return Err(AtomicOverwriteError::Protocol);
    }
    let mut packet = vec![0_u8; length];
    channel.read_exact(&mut packet)?;
    let packet_type = packet[0];
    packet.remove(0);
    Ok((packet_type, packet))
}

fn push_sftp_string(
    output: &mut Vec<u8>,
    value: &[u8],
) -> std::result::Result<(), AtomicOverwriteError> {
    let length = u32::try_from(value.len()).map_err(|_| AtomicOverwriteError::Protocol)?;
    let final_length = output
        .len()
        .checked_add(4)
        .and_then(|size| size.checked_add(value.len()))
        .filter(|size| *size <= MAX_SFTP_CONTROL_PACKET_BYTES)
        .ok_or(AtomicOverwriteError::Protocol)?;
    output.reserve(final_length - output.len());
    output.extend_from_slice(&length.to_be_bytes());
    output.extend_from_slice(value);
    Ok(())
}

struct SftpPacketCursor<'a> {
    bytes: &'a [u8],
    offset: usize,
}

impl<'a> SftpPacketCursor<'a> {
    const fn new(bytes: &'a [u8]) -> Self {
        Self { bytes, offset: 0 }
    }

    fn is_empty(&self) -> bool {
        self.offset == self.bytes.len()
    }

    fn read_u32(&mut self) -> std::result::Result<u32, AtomicOverwriteError> {
        let end = self
            .offset
            .checked_add(4)
            .filter(|end| *end <= self.bytes.len())
            .ok_or(AtomicOverwriteError::Protocol)?;
        let value = u32::from_be_bytes(
            self.bytes[self.offset..end]
                .try_into()
                .map_err(|_| AtomicOverwriteError::Protocol)?,
        );
        self.offset = end;
        Ok(value)
    }

    fn read_string(&mut self) -> std::result::Result<&'a [u8], AtomicOverwriteError> {
        let length =
            usize::try_from(self.read_u32()?).map_err(|_| AtomicOverwriteError::Protocol)?;
        let end = self
            .offset
            .checked_add(length)
            .filter(|end| *end <= self.bytes.len())
            .ok_or(AtomicOverwriteError::Protocol)?;
        let value = &self.bytes[self.offset..end];
        self.offset = end;
        Ok(value)
    }
}

fn temporary_upload_open_flags() -> OpenFlags {
    // EXCLUSIVE makes a UUID collision fail closed. Omitting TRUNCATE ensures a pre-existing
    // pathname can never be shortened, and 0600 keeps partial credentials/data private.
    OpenFlags::WRITE | OpenFlags::CREATE | OpenFlags::EXCLUSIVE
}

const fn max_encoded_file_bytes() -> usize {
    // RFC 4648 base64 is ceil(n / 3) * 4 bytes.
    MAX_FILE_BYTES.div_ceil(3) * 4
}

fn emit<R: Runtime, T: Serialize + Clone>(app: &AppHandle<R>, event: &str, payload: T) {
    // Renderer shutdown/event delivery failure must not panic a transfer worker.
    let _ = app.emit(event, payload);
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        sync::mpsc::{self, Receiver},
        time::Duration,
    };

    use serde_json::Value;

    use crate::{
        domain::{ProfileAuthenticationInput, SshProfileInput, TerminalPreferences},
        ephemeral::EphemeralConnectionStore,
        operation_admission::OperationAdmission,
        ssh::SshHostKeyStatus,
    };

    fn receive_upload_events(receiver: &Receiver<Value>) -> Vec<Value> {
        let deadline = Instant::now() + Duration::from_secs(20);
        let mut events = Vec::new();
        loop {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .expect("upload must finish before the integration-test deadline");
            let event = receiver
                .recv_timeout(remaining)
                .expect("receive upload IPC event");
            let completed = event["kind"] == "completed";
            events.push(event);
            if completed {
                return events;
            }
        }
    }

    fn read_remote_file(ssh: &SshService, profile_id: Uuid, remote_path: &str) -> Option<Vec<u8>> {
        let session = ssh
            .open_authenticated_for_terminal(profile_id)
            .expect("open authenticated fixture transport");
        let sftp = session.sftp().expect("open fixture SFTP");
        let mut file = match sftp.open(Path::new(remote_path)) {
            Ok(file) => file,
            Err(error) if error.code() == ErrorCode::SFTP(SSH_FX_NO_SUCH_FILE) => return None,
            Err(error) => panic!("read fixture file {remote_path}: {error}"),
        };
        let mut data = Vec::new();
        file.read_to_end(&mut data).expect("read fixture file");
        Some(data)
    }

    #[test]
    fn upload_names_cannot_escape_the_selected_remote_directory() {
        assert!(validate_file_name("report.csv").is_ok());
        for invalid in ["", ".", "..", "../secret", "a/b", "a\\b", "line\nfeed"] {
            assert!(matches!(
                validate_file_name(invalid),
                Err(NekoError::SshTransferInputInvalid)
            ));
        }
        assert_eq!(
            join_remote_path("/srv/uploads/", "report.csv").expect("safe path"),
            "/srv/uploads/report.csv"
        );
        assert_eq!(
            join_remote_path("/", "report.csv").expect("root path"),
            "/report.csv"
        );
        assert_eq!(
            temporary_upload_path("/srv/uploads/report.csv", Uuid::nil())
                .expect("safe temporary path"),
            "/srv/uploads/.report.csv.nekossh-00000000-0000-0000-0000-000000000000.part"
        );
        assert!(non_overwriting_rename_flags().is_empty());
        assert!(validate_destructive_remote_path("/srv/uploads/report.csv").is_ok());
        for invalid in ["/", ".", "..", "/srv/../etc", "/srv/./report.csv"] {
            assert!(matches!(
                validate_destructive_remote_path(invalid),
                Err(NekoError::SshTransferInputInvalid)
            ));
        }
    }

    #[test]
    fn encoded_upload_limit_cannot_overflow_or_bypass_the_decoded_limit() {
        assert_eq!(max_encoded_file_bytes(), 22_369_624);
        assert!(STANDARD.decode("aGVsbG8=").expect("valid base64").len() <= MAX_FILE_BYTES);
    }

    #[test]
    fn temporary_upload_is_private_exclusive_and_never_truncates() {
        let flags = temporary_upload_open_flags();
        assert_eq!(TEMPORARY_UPLOAD_MODE, 0o600);
        assert!(flags.contains(OpenFlags::WRITE));
        assert!(flags.contains(OpenFlags::CREATE));
        assert!(flags.contains(OpenFlags::EXCLUSIVE));
        assert!(!flags.contains(OpenFlags::READ));
        assert!(!flags.contains(OpenFlags::APPEND));
        assert!(!flags.contains(OpenFlags::TRUNCATE));
    }

    #[test]
    fn posix_rename_control_packets_are_strictly_bounded_and_parsed() {
        let mut encoded = Vec::new();
        push_sftp_string(&mut encoded, SFTP_POSIX_RENAME_EXTENSION).expect("bounded extension");
        push_sftp_string(&mut encoded, b"1").expect("bounded version");
        let mut cursor = SftpPacketCursor::new(&encoded);
        assert_eq!(
            cursor.read_string().expect("extension"),
            SFTP_POSIX_RENAME_EXTENSION
        );
        assert_eq!(
            cursor.read_string().expect("version"),
            SFTP_POSIX_RENAME_VERSION
        );
        assert!(cursor.is_empty());

        let truncated = &encoded[..encoded.len() - 1];
        let mut cursor = SftpPacketCursor::new(truncated);
        assert!(cursor.read_string().is_ok());
        assert_eq!(cursor.read_string(), Err(AtomicOverwriteError::Protocol));
        assert_eq!(
            push_sftp_string(&mut Vec::new(), &vec![0_u8; MAX_SFTP_CONTROL_PACKET_BYTES]),
            Err(AtomicOverwriteError::Protocol)
        );
    }

    #[test]
    fn profile_closing_signal_stops_late_transfer_side_effects() {
        let admission = OperationAdmission::new();
        let profile_id = Uuid::new_v4();
        let permit = admission.try_acquire(profile_id).expect("permit");
        let control = JobControl {
            cancelled: AtomicBool::new(false),
            operation_permit: permit.clone(),
            download_flow: DownloadOutputFlow::new(),
        };
        assert!(control.remote_effect_allowed());

        let close_guard = admission.begin_close(profile_id).expect("close guard");
        assert!(control.is_cancelled());
        assert!(!control.remote_effect_allowed());
        drop(permit);
        close_guard.retire();
    }

    #[test]
    fn concurrent_download_reservation_stops_at_job_limit() {
        let tracker = JobTracker::new(None);
        assert_eq!(
            tracker
                .reserve_download_bytes(MAX_JOB_BYTES as u64)
                .expect("exact max accepted"),
            MAX_JOB_BYTES as u64
        );
        assert!(matches!(
            tracker.reserve_download_bytes(1),
            Err(NekoError::SshTransferTotalTooLarge)
        ));
    }

    #[test]
    fn download_acknowledgement_only_releases_the_matching_chunk() {
        let flow = DownloadOutputFlow::new();
        {
            let mut state = flow.state.lock().expect("flow lock");
            state.pending_sequence = Some(12);
        }
        flow.acknowledge(11);
        assert_eq!(
            flow.state.lock().expect("flow lock").pending_sequence,
            Some(12)
        );
        flow.acknowledge(12);
        assert_eq!(flow.state.lock().expect("flow lock").pending_sequence, None);
    }

    #[test]
    fn transfer_events_use_camel_case_and_base64_payloads() {
        let value = serde_json::to_value(SshSftpDownloadChunk {
            job_id: Uuid::nil(),
            file_id: Uuid::nil(),
            name: "archive.tar".to_owned(),
            sequence: 7,
            offset: 0,
            data_base64: "YWJj".to_owned(),
        })
        .expect("serializes");
        assert_eq!(value["jobId"], Uuid::nil().to_string());
        assert_eq!(value["dataBase64"], "YWJj");
        assert_eq!(value["sequence"], 7);
        assert!(value.get("data_base64").is_none());

        let upload = serde_json::to_value(SshSftpUploadEvent::Progress(SshSftpTransferProgress {
            job_id: Uuid::nil(),
            file_id: Uuid::nil(),
            name: "report.csv".to_owned(),
            direction: SshSftpTransferDirection::Upload,
            file_bytes_transferred: 3,
            file_total_bytes: Some(8),
            job_bytes_transferred: 3,
            job_total_bytes: Some(8),
        }))
        .expect("upload event serializes");
        assert_eq!(upload["kind"], "progress");
        assert_eq!(upload["jobId"], Uuid::nil().to_string());
        assert_eq!(upload["direction"], "upload");
        assert_eq!(upload["jobBytesTransferred"], 3);
    }

    #[test]
    fn transfer_status_is_derived_from_final_file_counts() {
        assert_eq!(
            transfer_status(0, 0),
            SshSftpTransferStatus::Completed,
            "a cancellation requested after the final remote effect must not rewrite success"
        );
        assert_eq!(transfer_status(0, 1), SshSftpTransferStatus::Cancelled);
        assert_eq!(
            transfer_status(1, 1),
            SshSftpTransferStatus::CompletedWithErrors
        );
    }

    #[test]
    fn profile_cleanup_cancels_only_matching_transfer_jobs() {
        let store = Arc::new(EphemeralConnectionStore::new());
        let ssh = Arc::new(SshService::new(store));
        let service = SftpTransferService::new(ssh);
        let profile_id = Uuid::new_v4();
        let other_profile_id = Uuid::new_v4();
        let target_job_id = Uuid::new_v4();
        let other_job_id = Uuid::new_v4();
        let admission = OperationAdmission::new();
        let target_permit = admission.try_acquire(profile_id).expect("target permit");
        let other_permit = admission
            .try_acquire(other_profile_id)
            .expect("other permit");
        let target_control = Arc::new(JobControl {
            cancelled: AtomicBool::new(false),
            operation_permit: target_permit,
            download_flow: DownloadOutputFlow::new(),
        });
        let other_control = Arc::new(JobControl {
            cancelled: AtomicBool::new(false),
            operation_permit: other_permit,
            download_flow: DownloadOutputFlow::new(),
        });
        {
            let mut registry = service.registry.lock().expect("registry");
            registry
                .jobs
                .insert(target_job_id, Arc::clone(&target_control));
            registry
                .jobs
                .insert(other_job_id, Arc::clone(&other_control));
            registry.profiles.insert(target_job_id, profile_id);
            registry.profiles.insert(other_job_id, other_profile_id);
        }

        assert_eq!(
            service
                .profile_id_for_job(target_job_id)
                .expect("profile id"),
            profile_id
        );
        service
            .cancel_for_profile(profile_id)
            .expect("cancel matching profile");
        assert!(target_control.cancelled.load(Ordering::Acquire));
        assert!(!other_control.cancelled.load(Ordering::Acquire));
    }

    /// Runs only through `scripts/test-native-ssh-interoperability.mjs`. The fixture speaks SFTP
    /// v3 over a raw ssh2 server channel, so these assertions cover the production libssh2
    /// transport and the explicit posix-rename@openssh.com wire request.
    #[test]
    fn native_sftp_overwrite_interoperability() {
        if std::env::var("NEKOSSH_NATIVE_INTEROP").as_deref() != Ok("1") {
            return;
        }

        let host = std::env::var("NEKOSSH_NATIVE_INTEROP_HOST").expect("fixture host");
        let port = std::env::var("NEKOSSH_NATIVE_INTEROP_PORT")
            .expect("fixture port")
            .parse::<u16>()
            .expect("valid fixture port");
        let username = std::env::var("NEKOSSH_NATIVE_INTEROP_USERNAME").expect("fixture username");
        let password = std::env::var("NEKOSSH_NATIVE_INTEROP_PASSWORD").expect("fixture password");

        let owner = Uuid::new_v4();
        let store = Arc::new(EphemeralConnectionStore::new());
        let profile = store
            .create(
                owner,
                SshProfileInput {
                    label: "native-atomic-overwrite".to_owned(),
                    host,
                    port,
                    username,
                    authentication: ProfileAuthenticationInput::Password { password },
                    tags: Vec::new(),
                    terminal: TerminalPreferences::default(),
                },
            )
            .expect("create overwrite fixture profile");
        let ssh = Arc::new(SshService::new(store));
        let confirmation_id = match ssh.prepare(profile.id).expect("prepare fixture host") {
            SshHostKeyStatus::ConfirmationRequired {
                confirmation_id, ..
            } => confirmation_id,
            unexpected => panic!("fixture host must require confirmation: {unexpected:?}"),
        };
        assert!(matches!(
            ssh.trust_host_key(profile.id, confirmation_id)
                .expect("trust and authenticate fixture host"),
            SshHostKeyStatus::Trusted { .. }
        ));

        let service = SftpTransferService::new(Arc::clone(&ssh));
        let admission = OperationAdmission::new();

        let old_target = b"existing-target";
        let no_overwrite_source = b"no-overwrite-source";
        let no_overwrite = admission.try_acquire(profile.id).expect("admit rename");
        assert!(matches!(
            service.rename_remote(
                profile.id,
                "/workspace/no-overwrite-source.bin".to_owned(),
                "/workspace/no-overwrite-target.bin".to_owned(),
                false,
                no_overwrite.close_signal(),
            ),
            Err(NekoError::SshTransferFailed)
        ));
        drop(no_overwrite);
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/no-overwrite-target.bin").as_deref(),
            Some(old_target.as_slice())
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/no-overwrite-source.bin").as_deref(),
            Some(no_overwrite_source.as_slice())
        );

        let permission_source = b"permission-source";
        let permission_target = b"permission-target";
        let permission = admission
            .try_acquire(profile.id)
            .expect("admit permission-denied rename");
        assert!(matches!(
            service.rename_remote(
                profile.id,
                "/workspace/permission-source.bin".to_owned(),
                "/workspace/permission-denied.bin".to_owned(),
                false,
                permission.close_signal(),
            ),
            Err(NekoError::SshTransferFailed)
        ));
        drop(permission);
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/permission-source.bin").as_deref(),
            Some(permission_source.as_slice())
        );
        // Reading is allowed by the fixture; only LSTAT is denied. This proves the old bytes were
        // preserved rather than treating SSH_FX_PERMISSION_DENIED as "missing".
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/permission-denied.bin").as_deref(),
            Some(permission_target.as_slice())
        );

        let atomic_source = b"atomic-source";
        let atomic = admission
            .try_acquire(profile.id)
            .expect("admit atomic rename");
        service
            .rename_remote(
                profile.id,
                "/workspace/atomic-source.bin".to_owned(),
                "/workspace/atomic-target.bin".to_owned(),
                true,
                atomic.close_signal(),
            )
            .expect("atomically replace existing target");
        drop(atomic);
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/atomic-target.bin").as_deref(),
            Some(atomic_source.as_slice())
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/atomic-source.bin"),
            None
        );

        let unsupported_source = b"unsupported-source";
        let unsupported_target = b"unsupported-target";
        let unsupported = admission
            .try_acquire(profile.id)
            .expect("admit unsupported atomic rename");
        let error = service
            .rename_remote(
                profile.id,
                "/workspace/unsupported-source.bin".to_owned(),
                "/workspace/unsupported-target.bin".to_owned(),
                true,
                unsupported.close_signal(),
            )
            .expect_err("unsupported extension must fail closed");
        drop(unsupported);
        assert!(matches!(&error, NekoError::SshAtomicOverwriteUnsupported));
        assert_eq!(
            error.public_message(),
            "服务器不支持安全原子覆盖（posix-rename@openssh.com），原文件已保留。"
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/unsupported-source.bin").as_deref(),
            Some(unsupported_source.as_slice())
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/unsupported-target.bin").as_deref(),
            Some(unsupported_target.as_slice())
        );

        let start_upload =
            |name: &str, bytes: &[u8], overwrite: bool| -> (SshSftpTransferStarted, Vec<Value>) {
                let (sender, receiver) = mpsc::channel();
                let events = IpcChannel::new(move |body| {
                    let event = body
                        .deserialize::<Value>()
                        .expect("deserialize upload IPC event");
                    sender.send(event).expect("capture upload IPC event");
                    Ok(())
                });
                let permit = admission.try_acquire(profile.id).expect("admit upload");
                let started = service
                    .start_upload_headless(
                        profile.id,
                        "/workspace".to_owned(),
                        vec![SshSftpUploadInput {
                            name: name.to_owned(),
                            data_base64: STANDARD.encode(bytes),
                            overwrite,
                        }],
                        events,
                        permit,
                    )
                    .expect("start production upload");
                let events = receive_upload_events(&receiver);
                (started, events)
            };

        let (no_overwrite_upload, no_overwrite_events) =
            start_upload("upload-existing.bin", b"new-upload", false);
        assert_eq!(no_overwrite_upload.total_files, 1);
        assert_eq!(
            no_overwrite_events.last().expect("upload completion")["status"],
            "completed_with_errors"
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/upload-existing.bin").as_deref(),
            Some(b"old-upload".as_slice())
        );

        let (_, overwrite_events) = start_upload("upload-existing.bin", b"atomic-upload", true);
        assert_eq!(
            overwrite_events.last().expect("upload completion")["status"],
            "completed"
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/upload-existing.bin").as_deref(),
            Some(b"atomic-upload".as_slice())
        );

        let (_, unsupported_upload_events) =
            start_upload("upload-unsupported.bin", b"rejected-upload", true);
        assert_eq!(
            unsupported_upload_events.last().expect("upload completion")["status"],
            "completed_with_errors"
        );
        assert_eq!(
            read_remote_file(&ssh, profile.id, "/workspace/upload-unsupported.bin").as_deref(),
            Some(b"old-unsupported-upload".as_slice())
        );

        let listing = ssh
            .list_directory(profile.id, "/workspace".to_owned())
            .expect("list fixture after uploads");
        assert!(
            listing
                .entries
                .iter()
                .all(|entry| !entry.name.ends_with(".part")),
            "failed/successful uploads must not leave .part objects"
        );
    }
}
