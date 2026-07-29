//! Persistent SSH PTY sessions for the desktop terminal.
//!
//! Each terminal owns one worker thread and one verified `ssh2` channel.  The renderer can only
//! enqueue keystrokes, resize requests, or a close request by opaque session id; it never sees a
//! `Session`, host-key bytes, password, or private key.  The worker is the sole reader/writer of
//! its non-blocking channel, which avoids libssh2 lock contention between Tauri commands and
//! terminal output reads.

use std::{
    collections::{HashMap, VecDeque},
    io::{ErrorKind, Read, Write},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
        mpsc::{self, Receiver, SyncSender, TryRecvError},
    },
    thread,
    time::Duration,
};

use serde::Serialize;
use ssh2::{Channel, ExtendedData, Session};
use tauri::{AppHandle, Emitter};
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    error::{Result, VaultError},
    ssh::SshService,
};

pub const SSH_TERMINAL_OUTPUT_EVENT: &str = "ssh-terminal-output";
pub const SSH_TERMINAL_CLOSED_EVENT: &str = "ssh-terminal-closed";
pub const SSH_TERMINAL_ERROR_EVENT: &str = "ssh-terminal-error";

const DEFAULT_COLUMNS: u32 = 100;
const DEFAULT_ROWS: u32 = 30;
const MAX_COLUMNS: u32 = 500;
const MAX_ROWS: u32 = 300;
const MAX_ACTIVE_SESSIONS: usize = 12;
const MAX_INPUT_CHUNK_BYTES: usize = 16 * 1024;
const MAX_PENDING_CONTROLS: usize = 128;
const MAX_OUTPUT_CHUNK_BYTES: usize = 32 * 1024;
const WORKER_IDLE_WAIT: Duration = Duration::from_millis(8);

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshTerminalStarted {
    pub session_id: Uuid,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshTerminalOutput {
    pub session_id: Uuid,
    pub data: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshTerminalClosed {
    pub session_id: Uuid,
    pub exit_status: Option<i32>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshTerminalError {
    pub session_id: Uuid,
    /// Stable, non-sensitive error code.  Transport/library messages are never sent to the
    /// renderer because they can contain server-controlled content.
    pub code: &'static str,
}

pub struct TerminalService {
    ssh: Arc<SshService>,
    registry: Arc<Mutex<TerminalRegistry>>,
    accepting_starts: AtomicBool,
}

#[derive(Default)]
struct TerminalRegistry {
    sessions: HashMap<Uuid, TerminalSession>,
    /// Capacity reserved by start calls while they are doing the blocking SSH handshake.
    pending_starts: usize,
}

struct TerminalSession {
    profile_id: Uuid,
    controls: SyncSender<TerminalControl>,
    close_requested: Arc<AtomicBool>,
}

enum TerminalControl {
    Input(Zeroizing<Vec<u8>>),
    Resize { columns: u32, rows: u32 },
    Close,
}

struct PendingWrite {
    bytes: Zeroizing<Vec<u8>>,
    offset: usize,
}

impl TerminalService {
    pub fn new(ssh: Arc<SshService>) -> Self {
        Self {
            ssh,
            registry: Arc::new(Mutex::new(TerminalRegistry::default())),
            accepting_starts: AtomicBool::new(true),
        }
    }

    /// Opens an authenticated, host-key-verified interactive shell.  Unknown and mismatched
    /// keys fail through `SshService::open_authenticated_for_terminal`; callers must use the
    /// existing prepare/trust flow first.
    pub fn start(
        &self,
        app: AppHandle,
        profile_id: Uuid,
        columns: Option<u32>,
        rows: Option<u32>,
    ) -> Result<SshTerminalStarted> {
        let (columns, rows) = terminal_size(columns, rows)?;
        // Reserve before network I/O.  Otherwise repeated start requests could create unlimited
        // simultaneous DNS/TCP/handshake work before the active-session cap is observed.
        let mut reservation = self.reserve_start()?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let mut channel = session
            .channel_session()
            .map_err(|_| VaultError::SshTerminalStartFailed)?;
        channel
            .handle_extended_data(ExtendedData::Merge)
            .map_err(|_| VaultError::SshTerminalStartFailed)?;
        channel
            .request_pty("xterm-256color", None, Some((columns, rows, 0, 0)))
            .map_err(|_| VaultError::SshTerminalStartFailed)?;
        channel
            .shell()
            .map_err(|_| VaultError::SshTerminalStartFailed)?;
        // The worker alternates control messages with output reads.  Non-blocking libssh2 calls
        // ensure a quiet remote shell cannot hold the session mutex and starve terminal input.
        session.set_blocking(false);

        let session_id = Uuid::new_v4();
        let (controls, receiver) = mpsc::sync_channel(MAX_PENDING_CONTROLS);
        let close_requested = Arc::new(AtomicBool::new(false));
        {
            let mut registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
            // `close_all` flips this flag before it takes the same registry mutex.  If vault
            // lock raced with the handshake, do not let a newly authenticated PTY escape it.
            if !self.accepting_starts.load(Ordering::Acquire) {
                return Err(VaultError::Locked);
            }
            registry.sessions.insert(
                session_id,
                TerminalSession {
                    profile_id,
                    controls: controls.clone(),
                    close_requested: Arc::clone(&close_requested),
                },
            );
        }
        reservation.release();

        let registry = Arc::clone(&self.registry);
        let worker_close_requested = Arc::clone(&close_requested);
        if thread::Builder::new()
            .name(format!("nekossh-pty-{session_id}"))
            .spawn(move || {
                terminal_worker(
                    app,
                    session_id,
                    session,
                    channel,
                    receiver,
                    worker_close_requested,
                    registry,
                )
            })
            .is_err()
        {
            let mut registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
            registry.sessions.remove(&session_id);
            return Err(VaultError::SshTerminalStartFailed);
        }

        Ok(SshTerminalStarted { session_id })
    }

    /// Queues exact terminal bytes.  The receiver has a bounded queue so a compromised or
    /// malfunctioning renderer cannot grow Rust-process memory without limit.
    pub fn write(&self, session_id: Uuid, data: String) -> Result<()> {
        if data.len() > MAX_INPUT_CHUNK_BYTES {
            return Err(VaultError::SshTerminalInputTooLarge);
        }
        if data.is_empty() {
            return Ok(());
        }
        let bytes = Zeroizing::new(data.into_bytes());
        let session = self.session(session_id)?;
        if session.close_requested.load(Ordering::Acquire) {
            return Err(VaultError::SshTerminalSessionNotFound);
        }
        session
            .controls
            .try_send(TerminalControl::Input(bytes))
            .map_err(|error| match error {
                mpsc::TrySendError::Full(_) => VaultError::SshTerminalInputBackpressure,
                mpsc::TrySendError::Disconnected(_) => VaultError::SshTerminalSessionNotFound,
            })
    }

    pub fn resize(&self, session_id: Uuid, columns: u32, rows: u32) -> Result<()> {
        let (columns, rows) = terminal_size(Some(columns), Some(rows))?;
        let session = self.session(session_id)?;
        if session.close_requested.load(Ordering::Acquire) {
            return Err(VaultError::SshTerminalSessionNotFound);
        }
        session
            .controls
            .try_send(TerminalControl::Resize { columns, rows })
            .map_err(|error| match error {
                mpsc::TrySendError::Full(_) => VaultError::SshTerminalInputBackpressure,
                mpsc::TrySendError::Disconnected(_) => VaultError::SshTerminalSessionNotFound,
            })
    }

    /// Close is deliberately best-effort and non-blocking.  The atomic flag makes it reliable
    /// even if the bounded message queue is saturated by input.
    pub fn close(&self, session_id: Uuid) -> Result<()> {
        let session = self.session(session_id)?;
        session.close_requested.store(true, Ordering::Release);
        let _ = session.controls.try_send(TerminalControl::Close);
        Ok(())
    }

    /// Closes every shell owned by one ephemeral SSH profile before its in-memory credential is
    /// removed. Workers own their sockets and finish cleanup asynchronously.
    pub fn close_for_profile(&self, profile_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        for session in registry
            .sessions
            .values()
            .filter(|session| session.profile_id == profile_id)
        {
            session.close_requested.store(true, Ordering::Release);
            let _ = session.controls.try_send(TerminalControl::Close);
        }
        Ok(())
    }

    /// Called during application shutdown. Workers own their sockets and finish their cleanup
    /// asynchronously.
    pub fn close_all(&self) -> Result<()> {
        // Reject new starts before looking at the registry.  A concurrent start either observes
        // this here or inserts first, in which case this pass marks that new session for close.
        self.accepting_starts.store(false, Ordering::Release);
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        for session in registry.sessions.values() {
            session.close_requested.store(true, Ordering::Release);
            let _ = session.controls.try_send(TerminalControl::Close);
        }
        Ok(())
    }

    fn session(&self, session_id: Uuid) -> Result<TerminalSession> {
        let registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        let session = registry
            .sessions
            .get(&session_id)
            .ok_or(VaultError::SshTerminalSessionNotFound)?;
        Ok(TerminalSession {
            profile_id: session.profile_id,
            controls: session.controls.clone(),
            close_requested: Arc::clone(&session.close_requested),
        })
    }

    fn reserve_start(&self) -> Result<StartReservation> {
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(VaultError::Locked);
        }
        let mut registry = self.registry.lock().map_err(|_| VaultError::LockPoisoned)?;
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(VaultError::Locked);
        }
        if registry.sessions.len() + registry.pending_starts >= MAX_ACTIVE_SESSIONS {
            return Err(VaultError::SshTerminalSessionLimit);
        }
        registry.pending_starts += 1;
        Ok(StartReservation {
            registry: Arc::clone(&self.registry),
            active: true,
        })
    }
}

struct StartReservation {
    registry: Arc<Mutex<TerminalRegistry>>,
    active: bool,
}

impl StartReservation {
    fn release(&mut self) {
        if !self.active {
            return;
        }
        if let Ok(mut registry) = self.registry.lock() {
            registry.pending_starts = registry.pending_starts.saturating_sub(1);
        }
        self.active = false;
    }
}

impl Drop for StartReservation {
    fn drop(&mut self) {
        self.release();
    }
}

fn terminal_size(columns: Option<u32>, rows: Option<u32>) -> Result<(u32, u32)> {
    let columns = columns.unwrap_or(DEFAULT_COLUMNS);
    let rows = rows.unwrap_or(DEFAULT_ROWS);
    if !(1..=MAX_COLUMNS).contains(&columns) || !(1..=MAX_ROWS).contains(&rows) {
        return Err(VaultError::SshTerminalResizeInvalid);
    }
    Ok((columns, rows))
}

#[allow(clippy::too_many_arguments)]
fn terminal_worker(
    app: AppHandle,
    session_id: Uuid,
    // Keeping a Session owner in this worker makes the socket lifecycle explicit.  `Channel`
    // retains its own Arc internally, but it must never be handed to another thread.
    _session: Session,
    mut channel: Channel,
    receiver: Receiver<TerminalControl>,
    close_requested: Arc<AtomicBool>,
    registry: Arc<Mutex<TerminalRegistry>>,
) {
    let mut pending_writes = VecDeque::new();
    let mut failed = false;

    loop {
        if close_requested.load(Ordering::Acquire) {
            break;
        }

        match drain_controls(&receiver, &mut channel, &mut pending_writes) {
            Ok(WorkerControlState::Continue) => {}
            Ok(WorkerControlState::Close) => break,
            Err(()) => {
                failed = true;
                break;
            }
        }
        if let Err(()) = flush_pending_writes(&mut channel, &mut pending_writes) {
            failed = true;
            break;
        }
        match emit_available_output(&app, session_id, &mut channel) {
            Ok(OutputState::Open) => {}
            Ok(OutputState::Closed) => break,
            Err(()) => {
                failed = true;
                break;
            }
        }

        match receiver.recv_timeout(WORKER_IDLE_WAIT) {
            Ok(control) => match process_control(control, &mut channel, &mut pending_writes) {
                Ok(WorkerControlState::Continue) => {}
                Ok(WorkerControlState::Close) => break,
                Err(()) => {
                    failed = true;
                    break;
                }
            },
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }

    if failed {
        emit(
            &app,
            SSH_TERMINAL_ERROR_EVENT,
            SshTerminalError {
                session_id,
                code: "ssh_terminal_transport_failed",
            },
        );
    }
    let exit_status = channel.exit_status().ok();
    let _ = channel.close();
    if let Ok(mut active) = registry.lock() {
        active.sessions.remove(&session_id);
    }
    emit(
        &app,
        SSH_TERMINAL_CLOSED_EVENT,
        SshTerminalClosed {
            session_id,
            exit_status,
        },
    );
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WorkerControlState {
    Continue,
    Close,
}

fn drain_controls(
    receiver: &Receiver<TerminalControl>,
    channel: &mut Channel,
    pending_writes: &mut VecDeque<PendingWrite>,
) -> std::result::Result<WorkerControlState, ()> {
    loop {
        match receiver.try_recv() {
            Ok(control) => {
                if process_control(control, channel, pending_writes)? == WorkerControlState::Close {
                    return Ok(WorkerControlState::Close);
                }
            }
            Err(TryRecvError::Empty) => return Ok(WorkerControlState::Continue),
            Err(TryRecvError::Disconnected) => return Ok(WorkerControlState::Close),
        }
    }
}

fn process_control(
    control: TerminalControl,
    channel: &mut Channel,
    pending_writes: &mut VecDeque<PendingWrite>,
) -> std::result::Result<WorkerControlState, ()> {
    match control {
        TerminalControl::Input(bytes) => {
            pending_writes.push_back(PendingWrite { bytes, offset: 0 });
            Ok(WorkerControlState::Continue)
        }
        TerminalControl::Resize { columns, rows } => channel
            .request_pty_size(columns, rows, None, None)
            .map(|_| WorkerControlState::Continue)
            .map_err(|_| ()),
        TerminalControl::Close => Ok(WorkerControlState::Close),
    }
}

fn flush_pending_writes(
    channel: &mut Channel,
    pending_writes: &mut VecDeque<PendingWrite>,
) -> std::result::Result<(), ()> {
    while let Some(pending) = pending_writes.front_mut() {
        match channel.write(&pending.bytes[pending.offset..]) {
            Ok(0) => break,
            Ok(written) => {
                pending.offset += written;
                if pending.offset == pending.bytes.len() {
                    pending_writes.pop_front();
                }
            }
            Err(error) if error.kind() == ErrorKind::WouldBlock => break,
            Err(_) => return Err(()),
        }
    }
    match channel.flush() {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == ErrorKind::WouldBlock => Ok(()),
        Err(_) => Err(()),
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum OutputState {
    Open,
    Closed,
}

fn emit_available_output(
    app: &AppHandle,
    session_id: Uuid,
    channel: &mut Channel,
) -> std::result::Result<OutputState, ()> {
    let mut buffer = [0_u8; MAX_OUTPUT_CHUNK_BYTES];
    loop {
        match channel.read(&mut buffer) {
            Ok(0) => return Ok(OutputState::Closed),
            Ok(read) => emit(
                app,
                SSH_TERMINAL_OUTPUT_EVENT,
                SshTerminalOutput {
                    session_id,
                    data: String::from_utf8_lossy(&buffer[..read]).into_owned(),
                },
            ),
            Err(error) if error.kind() == ErrorKind::WouldBlock => return Ok(OutputState::Open),
            Err(_) => return Err(()),
        }
    }
}

fn emit<T: Serialize + Clone>(app: &AppHandle, event: &str, payload: T) {
    // Losing a renderer event (for example during app shutdown) must not panic or leak a worker.
    let _ = app.emit(event, payload);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn terminal_size_has_safe_defaults_and_limits() {
        assert_eq!(
            terminal_size(None, None).expect("defaults"),
            (DEFAULT_COLUMNS, DEFAULT_ROWS)
        );
        assert!(matches!(
            terminal_size(Some(0), Some(24)),
            Err(VaultError::SshTerminalResizeInvalid)
        ));
        assert!(matches!(
            terminal_size(Some(MAX_COLUMNS + 1), Some(24)),
            Err(VaultError::SshTerminalResizeInvalid)
        ));
    }

    #[test]
    fn terminal_events_use_camel_case_opaque_session_ids() {
        let session_id = Uuid::nil();
        let event = serde_json::to_value(SshTerminalOutput {
            session_id,
            data: "hello".to_owned(),
        })
        .expect("serializes");
        assert_eq!(event["sessionId"], session_id.to_string());
        assert_eq!(event["data"], "hello");
        assert!(event.get("session_id").is_none());
    }
}
