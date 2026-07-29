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
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering},
    },
    thread,
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::{Deserialize, Serialize};
use ssh2::RenameFlags;
use tauri::{AppHandle, Emitter};
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    error::{Result, VaultError},
    ssh::{SshService, validate_remote_path},
};

pub const SSH_SFTP_TRANSFER_PROGRESS_EVENT: &str = "ssh-sftp-transfer-progress";
pub const SSH_SFTP_TRANSFER_FILE_EVENT: &str = "ssh-sftp-transfer-file";
pub const SSH_SFTP_DOWNLOAD_CHUNK_EVENT: &str = "ssh-sftp-download-chunk";
pub const SSH_SFTP_TRANSFER_COMPLETE_EVENT: &str = "ssh-sftp-transfer-complete";

const MAX_ACTIVE_JOBS: usize = 4;
const MAX_WORKERS_PER_JOB: usize = 4;
const MAX_FILES_PER_JOB: usize = 32;
const MAX_FILE_BYTES: usize = 16 * 1024 * 1024;
const MAX_JOB_BYTES: usize = 64 * 1024 * 1024;
const MAX_FILE_NAME_BYTES: usize = 240;
const TRANSFER_CHUNK_BYTES: usize = 64 * 1024;

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
    pub offset: u64,
    /// Standard base64 keeps binary transfer data JSON-safe for Tauri events.  Chunks are capped
    /// at 64 KiB before encoding.
    pub data_base64: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshSftpTransferFileEvent {
    pub job_id: Uuid,
    pub file_id: Uuid,
    pub name: String,
    pub direction: SshSftpTransferDirection,
    pub status: SshSftpTransferStatus,
    /// Stable public error code only.  Remote server/library errors are intentionally omitted.
    pub code: Option<&'static str>,
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
    Failed(&'static str),
    Cancelled,
}

impl SftpTransferService {
    pub fn new(ssh: Arc<SshService>) -> Self {
        Self {
            ssh,
            registry: Arc::new(Mutex::new(TransferRegistry::default())),
            accepting_starts: AtomicBool::new(true),
        }
    }

    pub fn start_upload(
        &self,
        app: AppHandle,
        profile_id: Uuid,
        remote_directory: String,
        files: Vec<SshSftpUploadInput>,
    ) -> Result<SshSftpTransferStarted> {
        validate_remote_path(&remote_directory)?;
        let job_id = Uuid::new_v4();
        let control = self.reserve_job(job_id, profile_id)?;

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
                    return Err(VaultError::SshTransferFileTooLarge);
                }
                let bytes = STANDARD
                    .decode(encoded.as_bytes())
                    .map_err(|_| VaultError::SshTransferInputInvalid)?;
                if bytes.len() > MAX_FILE_BYTES {
                    return Err(VaultError::SshTransferFileTooLarge);
                }
                total_bytes = total_bytes
                    .checked_add(bytes.len())
                    .ok_or(VaultError::SshTransferTotalTooLarge)?;
                if total_bytes > MAX_JOB_BYTES {
                    return Err(VaultError::SshTransferTotalTooLarge);
                }

                let file_id = Uuid::new_v4();
                let remote_path = join_remote_path(&remote_directory, &name)?;
                // Two workers targeting the same final pathname would make overwrite behavior
                // nondeterministic, even with temporary upload names.  Reject it up front.
                if !destinations.insert(remote_path.clone()) {
                    return Err(VaultError::SshTransferInputInvalid);
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

            Ok::<_, VaultError>((items, tasks, total_bytes as u64))
        })();

        let (items, tasks, total_bytes) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.remove_job(job_id);
                return Err(error);
            }
        };
        if control.cancelled.load(Ordering::Acquire) {
            self.remove_job(job_id);
            return Err(VaultError::Locked);
        }

        let tracker = Arc::new(JobTracker::new(Some(total_bytes)));
        if let Err(error) = self.spawn_job(
            app,
            profile_id,
            job_id,
            SshSftpTransferDirection::Upload,
            tasks,
            control,
            tracker,
        ) {
            self.remove_job(job_id);
            return Err(error);
        }

        Ok(SshSftpTransferStarted {
            job_id,
            direction: SshSftpTransferDirection::Upload,
            total_files: items.len(),
            total_bytes: Some(total_bytes),
            items,
        })
    }

    pub fn start_download(
        &self,
        app: AppHandle,
        profile_id: Uuid,
        files: Vec<SshSftpDownloadInput>,
    ) -> Result<SshSftpTransferStarted> {
        let job_id = Uuid::new_v4();
        let control = self.reserve_job(job_id, profile_id)?;
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
                    return Err(VaultError::SshTransferInputInvalid);
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
            Ok::<_, VaultError>((items, tasks))
        })();

        let (items, tasks) = match prepared {
            Ok(prepared) => prepared,
            Err(error) => {
                self.remove_job(job_id);
                return Err(error);
            }
        };
        if control.cancelled.load(Ordering::Acquire) {
            self.remove_job(job_id);
            return Err(VaultError::Locked);
        }

        let tracker = Arc::new(JobTracker::new(None));
        if let Err(error) = self.spawn_job(
            app,
            profile_id,
            job_id,
            SshSftpTransferDirection::Download,
            tasks,
            control,
            tracker,
        ) {
            self.remove_job(job_id);
            return Err(error);
        }

        Ok(SshSftpTransferStarted {
            job_id,
            direction: SshSftpTransferDirection::Download,
            total_files: items.len(),
            total_bytes: None,
            items,
        })
    }

    pub fn cancel(&self, job_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        let control = registry
            .jobs
            .get(&job_id)
            .ok_or(VaultError::SshTransferJobNotFound)?;
        control.cancelled.store(true, Ordering::Release);
        Ok(())
    }

    /// Cancels every transfer that belongs to one ephemeral SSH profile. Workers observe the
    /// cancellation flag between bounded chunks and release their SSH sessions asynchronously.
    pub fn cancel_for_profile(&self, profile_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        for (job_id, control) in &registry.jobs {
            if registry.profiles.get(job_id) == Some(&profile_id) {
                control.cancelled.store(true, Ordering::Release);
            }
        }
        Ok(())
    }

    /// Creates one remote directory.  Recursive parent creation is intentionally not exposed:
    /// callers must present the exact destination and handle any missing parent explicitly.
    pub fn create_directory(&self, profile_id: Uuid, path: String) -> Result<()> {
        validate_remote_path(&path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| VaultError::SshTransferFailed)?;
        sftp.mkdir(Path::new(&path), 0o755)
            .map_err(|_| VaultError::SshTransferFailed)
    }

    /// Renames or moves one remote filesystem entry after a fresh host-key/authentication check.
    /// Replacing the destination is allowed only when the caller passes an explicit confirmation.
    pub fn rename_remote(
        &self,
        profile_id: Uuid,
        from_path: String,
        to_path: String,
        overwrite: bool,
    ) -> Result<()> {
        validate_destructive_remote_path(&from_path)?;
        validate_destructive_remote_path(&to_path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| VaultError::SshTransferFailed)?;
        sftp.rename(
            Path::new(&from_path),
            Path::new(&to_path),
            Some(rename_flags(overwrite)),
        )
        .map_err(|_| VaultError::SshTransferFailed)
    }

    /// Deletes one file/symlink or an empty directory only.  Recursive deletion is deliberately
    /// unavailable on the IPC surface, preventing a renderer from issuing `rm -rf` equivalents.
    pub fn delete_remote(&self, profile_id: Uuid, path: String) -> Result<()> {
        validate_destructive_remote_path(&path)?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| VaultError::SshTransferFailed)?;
        let stat = sftp
            .lstat(Path::new(&path))
            .map_err(|_| VaultError::SshTransferFailed)?;
        if stat.is_dir() {
            sftp.rmdir(Path::new(&path))
                .map_err(|_| VaultError::SshTransferFailed)
        } else {
            sftp.unlink(Path::new(&path))
                .map_err(|_| VaultError::SshTransferFailed)
        }
    }

    /// Called before vault lock.  Workers check the flag between chunks and their TCP streams
    /// retain the existing bounded SSH operation timeout for any in-flight network call.
    pub fn cancel_all(&self) -> Result<()> {
        self.accepting_starts.store(false, Ordering::Release);
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        for control in registry.jobs.values() {
            control.cancelled.store(true, Ordering::Release);
        }
        Ok(())
    }

    fn reserve_job(&self, job_id: Uuid, profile_id: Uuid) -> Result<Arc<JobControl>> {
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(VaultError::Locked);
        }
        let mut registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(VaultError::Locked);
        }
        if registry.jobs.len() >= MAX_ACTIVE_JOBS {
            return Err(VaultError::SshTransferJobLimit);
        }
        let control = Arc::new(JobControl {
            cancelled: AtomicBool::new(false),
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
        app: AppHandle,
        profile_id: Uuid,
        job_id: Uuid,
        direction: SshSftpTransferDirection,
        tasks: Vec<TransferTask>,
        control: Arc<JobControl>,
        tracker: Arc<JobTracker>,
    ) -> Result<()> {
        let worker_count = tasks.len().min(MAX_WORKERS_PER_JOB);
        let queue = Arc::new(Mutex::new(VecDeque::from(tasks)));
        let ssh = Arc::clone(&self.ssh);
        let registry = Arc::clone(&self.registry);
        thread::Builder::new()
            .name(format!("nekossh-sftp-job-{job_id}"))
            .spawn(move || {
                // One supervisor owns a small scoped worker pool.  Every worker gets a separate
                // SSH session, so libssh2 sessions are never accessed from more than one thread.
                thread::scope(|scope| {
                    for _ in 0..worker_count {
                        let worker_app = app.clone();
                        let worker_ssh = Arc::clone(&ssh);
                        let worker_queue = Arc::clone(&queue);
                        let worker_control = Arc::clone(&control);
                        let worker_tracker = Arc::clone(&tracker);
                        scope.spawn(move || {
                            transfer_worker(
                                worker_app,
                                worker_ssh,
                                profile_id,
                                job_id,
                                direction,
                                worker_queue,
                                worker_control,
                                worker_tracker,
                            );
                        });
                    }
                });
                finish_job(&app, job_id, direction, &control, &tracker, &registry);
            })
            .map_err(|_| VaultError::SshTransferFailed)?;
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
                .ok_or(VaultError::SshTransferTotalTooLarge)?;
            if next > MAX_JOB_BYTES as u64 {
                return Err(VaultError::SshTransferTotalTooLarge);
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
    app: AppHandle,
    ssh: Arc<SshService>,
    profile_id: Uuid,
    job_id: Uuid,
    direction: SshSftpTransferDirection,
    queue: Arc<Mutex<VecDeque<TransferTask>>>,
    control: Arc<JobControl>,
    tracker: Arc<JobTracker>,
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

        let (file_id, name) = match &task {
            TransferTask::Upload(task) => (task.file_id, task.name.clone()),
            TransferTask::Download(task) => (task.file_id, task.name.clone()),
        };
        let outcome = if control.cancelled.load(Ordering::Acquire) {
            TransferOutcome::Cancelled
        } else {
            match task {
                TransferTask::Upload(task) => {
                    upload_one(&app, &ssh, profile_id, job_id, &task, &control, &tracker)
                }
                TransferTask::Download(task) => {
                    download_one(&app, &ssh, profile_id, job_id, &task, &control, &tracker)
                }
            }
        };
        report_file_outcome(&app, job_id, file_id, name, direction, outcome, &tracker);
    }
}

fn upload_one(
    app: &AppHandle,
    ssh: &SshService,
    profile_id: Uuid,
    job_id: Uuid,
    task: &UploadTask,
    control: &JobControl,
    tracker: &JobTracker,
) -> TransferOutcome {
    let result = (|| -> Result<bool> {
        let session = ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| VaultError::SshTransferFailed)?;
        let temporary_path = temporary_upload_path(&task.remote_path, task.file_id)?;
        let mut file = sftp
            .create(Path::new(&temporary_path))
            .map_err(|_| VaultError::SshTransferFailed)?;
        let mut offset = 0_usize;
        let result = (|| -> Result<bool> {
            while offset < task.bytes.len() {
                if control.cancelled.load(Ordering::Acquire) {
                    return Ok(false);
                }
                let end = (offset + TRANSFER_CHUNK_BYTES).min(task.bytes.len());
                let written = file
                    .write(&task.bytes.as_ref()[offset..end])
                    .map_err(|_| VaultError::SshTransferFailed)?;
                if written == 0 {
                    return Err(VaultError::SshTransferFailed);
                }
                offset += written;
                let job_bytes = tracker.add_known_bytes(written as u64);
                emit(
                    app,
                    SSH_SFTP_TRANSFER_PROGRESS_EVENT,
                    SshSftpTransferProgress {
                        job_id,
                        file_id: task.file_id,
                        name: task.name.clone(),
                        direction: SshSftpTransferDirection::Upload,
                        file_bytes_transferred: offset as u64,
                        file_total_bytes: Some(task.bytes.len() as u64),
                        job_bytes_transferred: job_bytes,
                        job_total_bytes: tracker.total_bytes,
                    },
                );
            }
            file.flush().map_err(|_| VaultError::SshTransferFailed)?;
            if control.cancelled.load(Ordering::Acquire) {
                return Ok(false);
            }
            // Best effort: older SFTP servers may lack the OpenSSH fsync extension, but a
            // successful close/rename is still a valid completed upload.
            let _ = file.fsync();
            Ok(true)
        })();
        drop(file);
        match result {
            Ok(true) => {
                if sftp
                    .rename(
                        Path::new(&temporary_path),
                        Path::new(&task.remote_path),
                        Some(rename_flags(task.overwrite)),
                    )
                    .is_err()
                {
                    let _ = sftp.unlink(Path::new(&temporary_path));
                    return Err(VaultError::SshTransferFailed);
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

fn download_one(
    app: &AppHandle,
    ssh: &SshService,
    profile_id: Uuid,
    job_id: Uuid,
    task: &DownloadTask,
    control: &JobControl,
    tracker: &JobTracker,
) -> TransferOutcome {
    let result = (|| -> Result<bool> {
        let session = ssh.open_authenticated_for_terminal(profile_id)?;
        let sftp = session.sftp().map_err(|_| VaultError::SshTransferFailed)?;
        let stat = sftp
            .stat(Path::new(&task.remote_path))
            .map_err(|_| VaultError::SshTransferFailed)?;
        if stat.is_dir() || stat.size.is_some_and(|size| size > MAX_FILE_BYTES as u64) {
            return Err(VaultError::SshTransferFileTooLarge);
        }
        let file_total_bytes = stat.size;
        let mut file = sftp
            .open(Path::new(&task.remote_path))
            .map_err(|_| VaultError::SshTransferFailed)?;
        let mut buffer = [0_u8; TRANSFER_CHUNK_BYTES];
        let mut offset = 0_u64;
        loop {
            if control.cancelled.load(Ordering::Acquire) {
                return Ok(false);
            }
            let read = file
                .read(&mut buffer)
                .map_err(|_| VaultError::SshTransferFailed)?;
            if read == 0 {
                return Ok(true);
            }
            let next_offset = offset
                .checked_add(read as u64)
                .ok_or(VaultError::SshTransferFileTooLarge)?;
            if next_offset > MAX_FILE_BYTES as u64 {
                return Err(VaultError::SshTransferFileTooLarge);
            }
            let job_bytes = tracker.reserve_download_bytes(read as u64)?;
            emit(
                app,
                SSH_SFTP_DOWNLOAD_CHUNK_EVENT,
                SshSftpDownloadChunk {
                    job_id,
                    file_id: task.file_id,
                    name: task.name.clone(),
                    offset,
                    data_base64: STANDARD.encode(&buffer[..read]),
                },
            );
            offset = next_offset;
            emit(
                app,
                SSH_SFTP_TRANSFER_PROGRESS_EVENT,
                SshSftpTransferProgress {
                    job_id,
                    file_id: task.file_id,
                    name: task.name.clone(),
                    direction: SshSftpTransferDirection::Download,
                    file_bytes_transferred: offset,
                    file_total_bytes,
                    job_bytes_transferred: job_bytes,
                    job_total_bytes: tracker.total_bytes,
                },
            );
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
            if control.cancelled.load(Ordering::Acquire) && matches!(error, VaultError::Locked) {
                TransferOutcome::Cancelled
            } else {
                TransferOutcome::Failed(error.code())
            }
        }
    }
}

fn is_job_fatal(error: &VaultError) -> bool {
    matches!(
        error,
        VaultError::Locked
            | VaultError::SshHostKeyUntrusted
            | VaultError::SshHostKeyMismatch
            | VaultError::SshAuthenticationFailed
            | VaultError::SshAuthenticationUnsupported
            | VaultError::SshTransferTotalTooLarge
    )
}

fn report_file_outcome(
    app: &AppHandle,
    job_id: Uuid,
    file_id: Uuid,
    name: String,
    direction: SshSftpTransferDirection,
    outcome: TransferOutcome,
    tracker: &JobTracker,
) {
    let (status, code) = match outcome {
        TransferOutcome::Completed => {
            tracker.completed_files.fetch_add(1, Ordering::AcqRel);
            (SshSftpTransferStatus::Completed, None)
        }
        TransferOutcome::Failed(code) => {
            tracker.failed_files.fetch_add(1, Ordering::AcqRel);
            (SshSftpTransferStatus::CompletedWithErrors, Some(code))
        }
        TransferOutcome::Cancelled => {
            tracker.cancelled_files.fetch_add(1, Ordering::AcqRel);
            (SshSftpTransferStatus::Cancelled, None)
        }
    };
    emit(
        app,
        SSH_SFTP_TRANSFER_FILE_EVENT,
        SshSftpTransferFileEvent {
            job_id,
            file_id,
            name,
            direction,
            status,
            code,
        },
    );
}

fn finish_job(
    app: &AppHandle,
    job_id: Uuid,
    direction: SshSftpTransferDirection,
    control: &JobControl,
    tracker: &JobTracker,
    registry: &Arc<Mutex<TransferRegistry>>,
) {
    let completed_files = tracker.completed_files.load(Ordering::Acquire);
    let failed_files = tracker.failed_files.load(Ordering::Acquire);
    let cancelled_files = tracker.cancelled_files.load(Ordering::Acquire);
    let status = if failed_files > 0 {
        SshSftpTransferStatus::CompletedWithErrors
    } else if control.cancelled.load(Ordering::Acquire) || cancelled_files > 0 {
        SshSftpTransferStatus::Cancelled
    } else {
        SshSftpTransferStatus::Completed
    };
    if let Ok(mut active) = registry.lock() {
        active.jobs.remove(&job_id);
        active.profiles.remove(&job_id);
    }
    emit(
        app,
        SSH_SFTP_TRANSFER_COMPLETE_EVENT,
        SshSftpTransferCompleted {
            job_id,
            direction,
            status,
            completed_files,
            failed_files,
            cancelled_files,
            bytes_transferred: tracker.bytes_transferred.load(Ordering::Acquire),
            total_bytes: tracker.total_bytes,
        },
    );
}

fn validate_file_count(count: usize) -> Result<()> {
    if count == 0 || count > MAX_FILES_PER_JOB {
        return Err(VaultError::SshTransferInputInvalid);
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
        return Err(VaultError::SshTransferInputInvalid);
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
        return Err(VaultError::SshTransferInputInvalid);
    }
    Ok(())
}

fn temporary_upload_path(remote_path: &str, file_id: Uuid) -> Result<String> {
    let (directory, file_name) = remote_path
        .rsplit_once('/')
        .ok_or(VaultError::SshTransferInputInvalid)?;
    let directory = if directory.is_empty() { "/" } else { directory };
    let temporary_name = format!(".{file_name}.nekossh-{file_id}.part");
    join_remote_path(directory, &temporary_name)
}

fn rename_flags(overwrite: bool) -> RenameFlags {
    if overwrite {
        RenameFlags::OVERWRITE
    } else {
        RenameFlags::empty()
    }
}

const fn max_encoded_file_bytes() -> usize {
    // RFC 4648 base64 is ceil(n / 3) * 4 bytes.
    ((MAX_FILE_BYTES + 2) / 3) * 4
}

fn emit<T: Serialize + Clone>(app: &AppHandle, event: &str, payload: T) {
    // Renderer shutdown/event delivery failure must not panic a transfer worker.
    let _ = app.emit(event, payload);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn upload_names_cannot_escape_the_selected_remote_directory() {
        assert!(validate_file_name("report.csv").is_ok());
        for invalid in ["", ".", "..", "../secret", "a/b", "a\\b", "line\nfeed"] {
            assert!(matches!(
                validate_file_name(invalid),
                Err(VaultError::SshTransferInputInvalid)
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
        assert!(rename_flags(false).is_empty());
        assert!(rename_flags(true).contains(RenameFlags::OVERWRITE));
        assert!(validate_destructive_remote_path("/srv/uploads/report.csv").is_ok());
        for invalid in ["/", ".", "..", "/srv/../etc", "/srv/./report.csv"] {
            assert!(matches!(
                validate_destructive_remote_path(invalid),
                Err(VaultError::SshTransferInputInvalid)
            ));
        }
    }

    #[test]
    fn encoded_upload_limit_cannot_overflow_or_bypass_the_decoded_limit() {
        assert_eq!(max_encoded_file_bytes(), 22_369_624);
        assert!(STANDARD.decode("aGVsbG8=").expect("valid base64").len() <= MAX_FILE_BYTES);
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
            Err(VaultError::SshTransferTotalTooLarge)
        ));
    }

    #[test]
    fn transfer_events_use_camel_case_and_base64_payloads() {
        let value = serde_json::to_value(SshSftpDownloadChunk {
            job_id: Uuid::nil(),
            file_id: Uuid::nil(),
            name: "archive.tar".to_owned(),
            offset: 0,
            data_base64: "YWJj".to_owned(),
        })
        .expect("serializes");
        assert_eq!(value["jobId"], Uuid::nil().to_string());
        assert_eq!(value["dataBase64"], "YWJj");
        assert!(value.get("data_base64").is_none());
    }
}
