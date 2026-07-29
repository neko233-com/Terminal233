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
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc::{self, Receiver, SyncSender, TryRecvError},
    },
    thread,
    time::{Duration, Instant},
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use serde::Serialize;
use ssh2::{Channel, ExtendedData, Session};
use tauri::ipc::Channel as IpcChannel;
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    error::{NekoError, Result},
    operation_admission::ProfileCloseSignal,
    ssh::SshService,
};

const DEFAULT_COLUMNS: u32 = 100;
const DEFAULT_ROWS: u32 = 30;
const MAX_COLUMNS: u32 = 500;
const MAX_ROWS: u32 = 300;
const MAX_ACTIVE_SESSIONS: usize = 12;
const MAX_INPUT_CHUNK_BYTES: usize = 16 * 1024;
const MAX_PENDING_CONTROLS: usize = 32;
const MAX_PENDING_WRITE_BYTES: usize = 256 * 1024;
const MAX_OUTPUT_CHUNK_BYTES: usize = 32 * 1024;
const WORKER_IDLE_WAIT: Duration = Duration::from_millis(8);
const OUTPUT_ACK_TIMEOUT: Duration = Duration::from_secs(15);

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SshTerminalStarted {
    pub session_id: Uuid,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(
    tag = "kind",
    rename_all = "snake_case",
    rename_all_fields = "camelCase"
)]
pub enum SshTerminalEvent {
    /// Base64 preserves the exact PTY byte stream. In particular, invalid or split UTF-8 must not
    /// be replaced before xterm's streaming parser receives it.
    Output {
        session_id: Uuid,
        sequence: u64,
        data_base64: String,
    },
    Closed {
        session_id: Uuid,
        exit_status: Option<i32>,
    },
    Error {
        session_id: Uuid,
        /// Stable, non-sensitive error code. Transport/library messages may contain
        /// server-controlled content and never cross IPC.
        code: &'static str,
    },
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
    last_output_sequence: Arc<AtomicU64>,
    acknowledged_output_sequence: Arc<AtomicU64>,
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
        profile_id: Uuid,
        columns: Option<u32>,
        rows: Option<u32>,
        events: IpcChannel<SshTerminalEvent>,
        close_signal: ProfileCloseSignal,
    ) -> Result<SshTerminalStarted> {
        let (columns, rows) = terminal_size(columns, rows)?;
        // Reserve before network I/O.  Otherwise repeated start requests could create unlimited
        // simultaneous DNS/TCP/handshake work before the active-session cap is observed.
        let mut reservation = self.reserve_start()?;
        let session = self.ssh.open_authenticated_for_terminal(profile_id)?;
        let mut channel = session
            .channel_session()
            .map_err(|_| NekoError::SshTerminalStartFailed)?;
        channel
            .handle_extended_data(ExtendedData::Merge)
            .map_err(|_| NekoError::SshTerminalStartFailed)?;
        channel
            .request_pty("xterm-256color", None, Some((columns, rows, 0, 0)))
            .map_err(|_| NekoError::SshTerminalStartFailed)?;
        // A shell can execute remote startup scripts. Recheck after the handshake/PTY request and
        // linearize immediately before starting it so a closing profile cannot launch a late PTY.
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        channel
            .shell()
            .map_err(|_| NekoError::SshTerminalStartFailed)?;
        // The worker alternates control messages with output reads.  Non-blocking libssh2 calls
        // ensure a quiet remote shell cannot hold the session mutex and starve terminal input.
        session.set_blocking(false);

        let session_id = Uuid::new_v4();
        let (controls, receiver) = mpsc::sync_channel(MAX_PENDING_CONTROLS);
        let close_requested = Arc::new(AtomicBool::new(false));
        let last_output_sequence = Arc::new(AtomicU64::new(0));
        let acknowledged_output_sequence = Arc::new(AtomicU64::new(0));
        {
            let mut registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
            // `close_all` flips this flag before it takes the same registry mutex. If shutdown
            // raced with the handshake, do not let a newly authenticated PTY escape it.
            if !self.accepting_starts.load(Ordering::Acquire) {
                return Err(NekoError::Locked);
            }
            registry.sessions.insert(
                session_id,
                TerminalSession {
                    profile_id,
                    controls: controls.clone(),
                    close_requested: Arc::clone(&close_requested),
                    last_output_sequence: Arc::clone(&last_output_sequence),
                    acknowledged_output_sequence: Arc::clone(&acknowledged_output_sequence),
                },
            );
        }
        reservation.release();

        let registry = Arc::clone(&self.registry);
        let worker_close_requested = Arc::clone(&close_requested);
        let worker_last_output_sequence = Arc::clone(&last_output_sequence);
        let worker_acknowledged_output_sequence = Arc::clone(&acknowledged_output_sequence);
        if thread::Builder::new()
            .name(format!("nekossh-pty-{session_id}"))
            .spawn(move || {
                terminal_worker(
                    session_id,
                    session,
                    channel,
                    receiver,
                    worker_close_requested,
                    worker_last_output_sequence,
                    worker_acknowledged_output_sequence,
                    registry,
                    events,
                )
            })
            .is_err()
        {
            let mut registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
            registry.sessions.remove(&session_id);
            return Err(NekoError::SshTerminalStartFailed);
        }

        Ok(SshTerminalStarted { session_id })
    }

    /// Queues exact terminal bytes.  The receiver has a bounded queue so a compromised or
    /// malfunctioning renderer cannot grow Rust-process memory without limit.
    pub fn write(&self, session_id: Uuid, data: String) -> Result<()> {
        if data.len() > MAX_INPUT_CHUNK_BYTES {
            return Err(NekoError::SshTerminalInputTooLarge);
        }
        if data.is_empty() {
            return Ok(());
        }
        let bytes = Zeroizing::new(data.into_bytes());
        let session = self.session(session_id)?;
        if session.close_requested.load(Ordering::Acquire) {
            return Err(NekoError::SshTerminalSessionNotFound);
        }
        session
            .controls
            .try_send(TerminalControl::Input(bytes))
            .map_err(|error| match error {
                mpsc::TrySendError::Full(_) => NekoError::SshTerminalInputBackpressure,
                mpsc::TrySendError::Disconnected(_) => NekoError::SshTerminalSessionNotFound,
            })
    }

    pub fn resize(&self, session_id: Uuid, columns: u32, rows: u32) -> Result<()> {
        let (columns, rows) = terminal_size(Some(columns), Some(rows))?;
        let session = self.session(session_id)?;
        if session.close_requested.load(Ordering::Acquire) {
            return Err(NekoError::SshTerminalSessionNotFound);
        }
        session
            .controls
            .try_send(TerminalControl::Resize { columns, rows })
            .map_err(|error| match error {
                mpsc::TrySendError::Full(_) => NekoError::SshTerminalInputBackpressure,
                mpsc::TrySendError::Disconnected(_) => NekoError::SshTerminalSessionNotFound,
            })
    }

    /// Acknowledges one output chunk only after xterm has parsed it. A future or stale sequence is
    /// ignored instead of advancing the watermark, so an untrusted renderer cannot disable output
    /// flow control by acknowledging data that Rust has not sent.
    pub fn acknowledge_output(&self, session_id: Uuid, sequence: u64) -> Result<()> {
        let session = self.session(session_id)?;
        if session.close_requested.load(Ordering::Acquire) {
            return Err(NekoError::SshTerminalSessionNotFound);
        }
        if sequence != 0 && session.last_output_sequence.load(Ordering::Acquire) == sequence {
            session
                .acknowledged_output_sequence
                .store(sequence, Ordering::Release);
        }
        Ok(())
    }

    /// Close is deliberately best-effort and non-blocking.  The atomic flag makes it reliable
    /// even if the bounded message queue is saturated by input.
    pub fn close(&self, session_id: Uuid) -> Result<()> {
        let session = self.session(session_id)?;
        session.close_requested.store(true, Ordering::Release);
        let _ = session.controls.try_send(TerminalControl::Close);
        Ok(())
    }

    /// Resolves the owning ephemeral profile without exposing any credential or transport state.
    /// Command handlers use this to refresh the idle deadline for actual terminal activity.
    pub fn profile_id_for_session(&self, session_id: Uuid) -> Result<Uuid> {
        self.session(session_id).map(|session| session.profile_id)
    }

    /// Closes every shell owned by one ephemeral SSH profile before its in-memory credential is
    /// removed. Workers own their sockets and finish cleanup asynchronously.
    pub fn close_for_profile(&self, profile_id: Uuid) -> Result<()> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
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
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        for session in registry.sessions.values() {
            session.close_requested.store(true, Ordering::Release);
            let _ = session.controls.try_send(TerminalControl::Close);
        }
        Ok(())
    }

    fn session(&self, session_id: Uuid) -> Result<TerminalSession> {
        let registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        let session = registry
            .sessions
            .get(&session_id)
            .ok_or(NekoError::SshTerminalSessionNotFound)?;
        Ok(TerminalSession {
            profile_id: session.profile_id,
            controls: session.controls.clone(),
            close_requested: Arc::clone(&session.close_requested),
            last_output_sequence: Arc::clone(&session.last_output_sequence),
            acknowledged_output_sequence: Arc::clone(&session.acknowledged_output_sequence),
        })
    }

    fn reserve_start(&self) -> Result<StartReservation> {
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(NekoError::Locked);
        }
        let mut registry = self.registry.lock().map_err(|_| NekoError::LockPoisoned)?;
        if !self.accepting_starts.load(Ordering::Acquire) {
            return Err(NekoError::Locked);
        }
        if registry.sessions.len() + registry.pending_starts >= MAX_ACTIVE_SESSIONS {
            return Err(NekoError::SshTerminalSessionLimit);
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
        return Err(NekoError::SshTerminalResizeInvalid);
    }
    Ok((columns, rows))
}

#[allow(clippy::too_many_arguments)]
fn terminal_worker(
    session_id: Uuid,
    // Keeping a Session owner in this worker makes the socket lifecycle explicit.  `Channel`
    // retains its own Arc internally, but it must never be handed to another thread.
    _session: Session,
    mut channel: Channel,
    receiver: Receiver<TerminalControl>,
    close_requested: Arc<AtomicBool>,
    last_output_sequence: Arc<AtomicU64>,
    acknowledged_output_sequence: Arc<AtomicU64>,
    registry: Arc<Mutex<TerminalRegistry>>,
    events: IpcChannel<SshTerminalEvent>,
) {
    let mut pending_writes = VecDeque::new();
    let mut output_flow = OutputFlow::new();
    let mut failure_code = None;

    loop {
        if close_requested.load(Ordering::Acquire) {
            break;
        }

        match drain_controls(&receiver, &mut channel, &mut pending_writes) {
            Ok(WorkerControlState::Continue) => {}
            Ok(WorkerControlState::Close) => break,
            Err(()) => {
                failure_code = Some("ssh_terminal_transport_failed");
                break;
            }
        }
        if let Err(()) = flush_pending_writes(&mut channel, &mut pending_writes) {
            failure_code = Some("ssh_terminal_transport_failed");
            break;
        }
        match poll_available_output(
            &events,
            session_id,
            &mut channel,
            &last_output_sequence,
            &acknowledged_output_sequence,
            &mut output_flow,
        ) {
            Ok(OutputState::Open) => {}
            Ok(OutputState::Closed) => break,
            Err(code) => {
                failure_code = Some(code);
                break;
            }
        }

        if pending_write_bytes(&pending_writes) >= MAX_PENDING_WRITE_BYTES {
            thread::sleep(WORKER_IDLE_WAIT);
            continue;
        }
        match receiver.recv_timeout(WORKER_IDLE_WAIT) {
            Ok(control) => match process_control(control, &mut channel, &mut pending_writes) {
                Ok(WorkerControlState::Continue) => {}
                Ok(WorkerControlState::Close) => break,
                Err(()) => {
                    failure_code = Some("ssh_terminal_transport_failed");
                    break;
                }
            },
            Err(mpsc::RecvTimeoutError::Timeout) => {}
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }

    if let Some(code) = failure_code {
        let _ = events.send(SshTerminalEvent::Error { session_id, code });
    }
    let exit_status = channel.exit_status().ok();
    let _ = channel.close();
    if let Ok(mut active) = registry.lock() {
        active.sessions.remove(&session_id);
    }
    let _ = events.send(SshTerminalEvent::Closed {
        session_id,
        exit_status,
    });
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
        if pending_write_bytes(pending_writes) >= MAX_PENDING_WRITE_BYTES {
            return Ok(WorkerControlState::Continue);
        }
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

fn pending_write_bytes(pending_writes: &VecDeque<PendingWrite>) -> usize {
    pending_writes
        .iter()
        .map(|pending| pending.bytes.len().saturating_sub(pending.offset))
        .sum()
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

struct PendingOutputAck {
    sequence: u64,
    sent_at: Instant,
}

struct OutputFlow {
    next_sequence: u64,
    pending_ack: Option<PendingOutputAck>,
}

impl OutputFlow {
    const fn new() -> Self {
        Self {
            next_sequence: 1,
            pending_ack: None,
        }
    }
}

fn poll_available_output(
    events: &IpcChannel<SshTerminalEvent>,
    session_id: Uuid,
    channel: &mut Channel,
    last_output_sequence: &AtomicU64,
    acknowledged_output_sequence: &AtomicU64,
    flow: &mut OutputFlow,
) -> std::result::Result<OutputState, &'static str> {
    if let Some(pending) = &flow.pending_ack {
        if acknowledged_output_sequence.load(Ordering::Acquire) == pending.sequence {
            flow.pending_ack = None;
        } else if pending.sent_at.elapsed() >= OUTPUT_ACK_TIMEOUT {
            return Err("ssh_terminal_output_backpressure");
        } else {
            return Ok(OutputState::Open);
        }
    }

    let mut buffer = [0_u8; MAX_OUTPUT_CHUNK_BYTES];
    match channel.read(&mut buffer) {
        Ok(0) => Ok(OutputState::Closed),
        Ok(read) => {
            let sequence = flow.next_sequence;
            let next_sequence = sequence
                .checked_add(1)
                .ok_or("ssh_terminal_output_sequence_exhausted")?;
            last_output_sequence.store(sequence, Ordering::Release);
            events
                .send(SshTerminalEvent::Output {
                    session_id,
                    sequence,
                    data_base64: STANDARD.encode(&buffer[..read]),
                })
                .map_err(|_| "ssh_terminal_output_delivery_failed")?;
            flow.next_sequence = next_sequence;
            flow.pending_ack = Some(PendingOutputAck {
                sequence,
                sent_at: Instant::now(),
            });
            Ok(OutputState::Open)
        }
        Err(error) if error.kind() == ErrorKind::WouldBlock => Ok(OutputState::Open),
        Err(_) => Err("ssh_terminal_transport_failed"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ephemeral::EphemeralConnectionStore;

    #[test]
    fn terminal_size_has_safe_defaults_and_limits() {
        assert_eq!(
            terminal_size(None, None).expect("defaults"),
            (DEFAULT_COLUMNS, DEFAULT_ROWS)
        );
        assert!(matches!(
            terminal_size(Some(0), Some(24)),
            Err(NekoError::SshTerminalResizeInvalid)
        ));
        assert!(matches!(
            terminal_size(Some(MAX_COLUMNS + 1), Some(24)),
            Err(NekoError::SshTerminalResizeInvalid)
        ));
    }

    #[test]
    fn terminal_events_preserve_bytes_and_use_camel_case_session_ids() {
        let session_id = Uuid::nil();
        let bytes = [0x00, 0xff, 0xc3, 0x28];
        let event = serde_json::to_value(SshTerminalEvent::Output {
            session_id,
            sequence: 7,
            data_base64: STANDARD.encode(bytes),
        })
        .expect("serializes");
        assert_eq!(event["kind"], "output");
        assert_eq!(event["sessionId"], session_id.to_string());
        assert_eq!(event["sequence"], 7);
        assert_eq!(
            STANDARD
                .decode(event["dataBase64"].as_str().expect("base64"))
                .expect("valid base64"),
            bytes
        );
        assert!(event.get("session_id").is_none());
        assert!(event.get("data_base64").is_none());
    }

    #[test]
    fn output_flow_allows_only_one_unacknowledged_chunk() {
        let mut flow = OutputFlow::new();
        assert_eq!(flow.next_sequence, 1);
        assert!(flow.pending_ack.is_none());
        flow.pending_ack = Some(PendingOutputAck {
            sequence: 1,
            sent_at: Instant::now(),
        });
        assert_eq!(
            flow.pending_ack.as_ref().map(|pending| pending.sequence),
            Some(1)
        );
    }

    #[test]
    fn output_acknowledgement_cannot_advance_past_the_last_sent_chunk() {
        let store = Arc::new(EphemeralConnectionStore::new());
        let ssh = Arc::new(SshService::new(store));
        let service = TerminalService::new(ssh);
        let session_id = Uuid::new_v4();
        let (controls, _receiver) = mpsc::sync_channel(1);
        let last_output_sequence = Arc::new(AtomicU64::new(4));
        let acknowledged_output_sequence = Arc::new(AtomicU64::new(0));
        service.registry.lock().expect("registry").sessions.insert(
            session_id,
            TerminalSession {
                profile_id: Uuid::nil(),
                controls,
                close_requested: Arc::new(AtomicBool::new(false)),
                last_output_sequence,
                acknowledged_output_sequence: Arc::clone(&acknowledged_output_sequence),
            },
        );

        service
            .acknowledge_output(session_id, u64::MAX)
            .expect("future acknowledgement is ignored");
        assert_eq!(acknowledged_output_sequence.load(Ordering::Acquire), 0);
        service
            .acknowledge_output(session_id, 4)
            .expect("current output is acknowledged");
        assert_eq!(acknowledged_output_sequence.load(Ordering::Acquire), 4);
    }

    #[test]
    fn profile_cleanup_closes_only_matching_terminal_sessions() {
        let store = Arc::new(EphemeralConnectionStore::new());
        let ssh = Arc::new(SshService::new(store));
        let service = TerminalService::new(ssh);
        let profile_id = Uuid::new_v4();
        let other_profile_id = Uuid::new_v4();
        let target_session_id = Uuid::new_v4();
        let other_session_id = Uuid::new_v4();
        let target_closed = Arc::new(AtomicBool::new(false));
        let other_closed = Arc::new(AtomicBool::new(false));
        let (target_controls, _target_receiver) = mpsc::sync_channel(1);
        let (other_controls, _other_receiver) = mpsc::sync_channel(1);
        {
            let mut registry = service.registry.lock().expect("registry");
            registry.sessions.insert(
                target_session_id,
                TerminalSession {
                    profile_id,
                    controls: target_controls,
                    close_requested: Arc::clone(&target_closed),
                    last_output_sequence: Arc::new(AtomicU64::new(0)),
                    acknowledged_output_sequence: Arc::new(AtomicU64::new(0)),
                },
            );
            registry.sessions.insert(
                other_session_id,
                TerminalSession {
                    profile_id: other_profile_id,
                    controls: other_controls,
                    close_requested: Arc::clone(&other_closed),
                    last_output_sequence: Arc::new(AtomicU64::new(0)),
                    acknowledged_output_sequence: Arc::new(AtomicU64::new(0)),
                },
            );
        }

        assert_eq!(
            service
                .profile_id_for_session(target_session_id)
                .expect("profile id"),
            profile_id
        );
        service
            .close_for_profile(profile_id)
            .expect("close matching profile");
        assert!(target_closed.load(Ordering::Acquire));
        assert!(!other_closed.load(Ordering::Acquire));
    }
}
