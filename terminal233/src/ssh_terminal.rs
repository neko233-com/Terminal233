//! Persistent SSH PTY sessions for the desktop terminal.
//!
//! Each terminal owns one verified pure-Rust `puressh` channel. The renderer can only
//! enqueue keystrokes, resize requests, or a close request by opaque session id; it never sees a
//! client, host-key bytes, password, or private key. A dedicated reader and bounded controller
//! share only puressh's channel-safe handle, while a cloned TCP handle makes close deterministic.

use std::{
    collections::HashMap,
    io::Read,
    net::{Shutdown, TcpStream},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc::{self, Receiver, SyncSender},
    },
    thread,
    time::{Duration, Instant},
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use puressh::shared::{OwnedChannelStream, SharedClient};
use serde::Serialize;
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    error::{NekoError, Result},
    ipc::Channel as IpcChannel,
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
const MAX_OUTPUT_CHUNK_BYTES: usize = 32 * 1024;
const WORKER_IDLE_WAIT: Duration = Duration::from_millis(8);
const SSH_PUMP_TIMEOUT: Duration = Duration::from_millis(50);
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
        let (client, shutdown_stream) = session.into_parts();
        let shared = SharedClient::from(client);
        shared
            .set_read_timeout(Some(SSH_PUMP_TIMEOUT))
            .map_err(|_| NekoError::SshTerminalStartFailed)?;
        // A shell can execute remote startup scripts. Recheck after the handshake/PTY request and
        // linearize immediately before starting it so a closing profile cannot launch a late PTY.
        if !close_signal.try_begin_remote_effect() {
            return Err(NekoError::ProfileNotFound);
        }
        let channel = shared
            .shell_stream("xterm-256color", columns, rows, 0, 0, Vec::new())
            .map_err(|_| NekoError::SshTerminalStartFailed)?;

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
            .name(format!("terminal233-pty-{session_id}"))
            .spawn(move || {
                terminal_worker(
                    session_id,
                    shared,
                    channel,
                    shutdown_stream,
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
    shared: SharedClient,
    mut channel: OwnedChannelStream,
    shutdown_stream: TcpStream,
    receiver: Receiver<TerminalControl>,
    close_requested: Arc<AtomicBool>,
    last_output_sequence: Arc<AtomicU64>,
    acknowledged_output_sequence: Arc<AtomicU64>,
    registry: Arc<Mutex<TerminalRegistry>>,
    events: IpcChannel<SshTerminalEvent>,
) {
    let channel_id = channel.channel_id();
    let failure_code = Arc::new(Mutex::new(None::<&'static str>));
    let controller = shutdown_stream
        .try_clone()
        .ok()
        .and_then(|controller_shutdown| {
            let controller_shared = shared.clone();
            let controller_close = Arc::clone(&close_requested);
            let controller_failure = Arc::clone(&failure_code);
            thread::Builder::new()
                .name(format!("terminal233-pty-control-{session_id}"))
                .spawn(move || {
                    terminal_control_worker(
                        controller_shared,
                        channel_id,
                        controller_shutdown,
                        receiver,
                        controller_close,
                        controller_failure,
                    );
                })
                .ok()
        });
    if controller.is_none() {
        set_terminal_failure(&failure_code, "ssh_terminal_transport_failed");
        close_requested.store(true, Ordering::Release);
        let _ = shutdown_stream.shutdown(Shutdown::Both);
    }

    let mut output_flow = OutputFlow::new();
    loop {
        if close_requested.load(Ordering::Acquire) {
            break;
        }
        match wait_for_output_ack(
            &acknowledged_output_sequence,
            &close_requested,
            &mut output_flow,
        ) {
            Ok(true) => {}
            Ok(false) => break,
            Err(code) => {
                set_terminal_failure(&failure_code, code);
                close_requested.store(true, Ordering::Release);
                let _ = shutdown_stream.shutdown(Shutdown::Both);
                break;
            }
        }

        let mut buffer = [0_u8; MAX_OUTPUT_CHUNK_BYTES];
        let read = match channel.read(&mut buffer) {
            Ok(0) => break,
            Ok(read) => read,
            Err(_) if close_requested.load(Ordering::Acquire) => break,
            Err(_) => {
                set_terminal_failure(&failure_code, "ssh_terminal_transport_failed");
                close_requested.store(true, Ordering::Release);
                break;
            }
        };
        if let Err(code) = send_terminal_output(
            &events,
            session_id,
            &buffer[..read],
            &last_output_sequence,
            &mut output_flow,
        ) {
            set_terminal_failure(&failure_code, code);
            close_requested.store(true, Ordering::Release);
            let _ = shutdown_stream.shutdown(Shutdown::Both);
            break;
        }
    }

    let exit_status = channel.exit_status();
    close_requested.store(true, Ordering::Release);
    let _ = shutdown_stream.shutdown(Shutdown::Both);
    drop(channel);
    if let Some(controller) = controller {
        let _ = controller.join();
    }
    if let Some(code) = failure_code.lock().ok().and_then(|failure| *failure) {
        let _ = events.send(SshTerminalEvent::Error { session_id, code });
    }
    if let Ok(mut active) = registry.lock() {
        active.sessions.remove(&session_id);
    }
    let _ = events.send(SshTerminalEvent::Closed {
        session_id,
        exit_status,
    });
}

fn terminal_control_worker(
    shared: SharedClient,
    channel_id: u32,
    shutdown_stream: TcpStream,
    receiver: Receiver<TerminalControl>,
    close_requested: Arc<AtomicBool>,
    failure_code: Arc<Mutex<Option<&'static str>>>,
) {
    while !close_requested.load(Ordering::Acquire) {
        let control = match receiver.recv_timeout(WORKER_IDLE_WAIT) {
            Ok(control) => control,
            Err(mpsc::RecvTimeoutError::Timeout) => continue,
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        };
        let result = match control {
            TerminalControl::Input(bytes) => {
                let mut offset = 0;
                while offset < bytes.len() {
                    match shared.channel_send_data(channel_id, &bytes[offset..]) {
                        Ok(0) => {
                            set_terminal_failure(&failure_code, "ssh_terminal_transport_failed");
                            close_requested.store(true, Ordering::Release);
                            break;
                        }
                        Ok(written) => offset += written,
                        Err(_) => {
                            set_terminal_failure(&failure_code, "ssh_terminal_transport_failed");
                            close_requested.store(true, Ordering::Release);
                            break;
                        }
                    }
                }
                Ok(())
            }
            TerminalControl::Resize { columns, rows } => shared
                .send_window_change(channel_id, columns, rows, 0, 0)
                .map_err(|_| ()),
            TerminalControl::Close => {
                close_requested.store(true, Ordering::Release);
                Ok(())
            }
        };
        if result.is_err() {
            set_terminal_failure(&failure_code, "ssh_terminal_transport_failed");
            close_requested.store(true, Ordering::Release);
            break;
        }
    }
    let _ = shared.channel_send_eof(channel_id);
    let _ = shutdown_stream.shutdown(Shutdown::Both);
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

fn wait_for_output_ack(
    acknowledged_output_sequence: &AtomicU64,
    close_requested: &AtomicBool,
    flow: &mut OutputFlow,
) -> std::result::Result<bool, &'static str> {
    while let Some(pending) = &flow.pending_ack {
        if acknowledged_output_sequence.load(Ordering::Acquire) == pending.sequence {
            flow.pending_ack = None;
            break;
        }
        if pending.sent_at.elapsed() >= OUTPUT_ACK_TIMEOUT {
            return Err("ssh_terminal_output_backpressure");
        }
        if close_requested.load(Ordering::Acquire) {
            return Ok(false);
        }
        thread::sleep(WORKER_IDLE_WAIT);
    }
    Ok(true)
}

fn send_terminal_output(
    events: &IpcChannel<SshTerminalEvent>,
    session_id: Uuid,
    bytes: &[u8],
    last_output_sequence: &AtomicU64,
    flow: &mut OutputFlow,
) -> std::result::Result<(), &'static str> {
    let sequence = flow.next_sequence;
    let next_sequence = sequence
        .checked_add(1)
        .ok_or("ssh_terminal_output_sequence_exhausted")?;
    last_output_sequence.store(sequence, Ordering::Release);
    events
        .send(SshTerminalEvent::Output {
            session_id,
            sequence,
            data_base64: STANDARD.encode(bytes),
        })
        .map_err(|_| "ssh_terminal_output_delivery_failed")?;
    flow.next_sequence = next_sequence;
    flow.pending_ack = Some(PendingOutputAck {
        sequence,
        sent_at: Instant::now(),
    });
    Ok(())
}

fn set_terminal_failure(failure_code: &Mutex<Option<&'static str>>, code: &'static str) {
    if let Ok(mut failure) = failure_code.lock()
        && failure.is_none()
    {
        *failure = Some(code);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::domain::{ProfileAuthenticationInput, SshProfileInput, TerminalPreferences};
    use crate::ephemeral::EphemeralConnectionStore;
    use crate::operation_admission::OperationAdmission;
    use crate::ssh::SshHostKeyStatus;
    use serde_json::Value;

    #[test]
    fn native_terminal_interoperability() {
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
                    label: "native-terminal".to_owned(),
                    host,
                    port,
                    username,
                    authentication: ProfileAuthenticationInput::Password { password },
                    tags: Vec::new(),
                    terminal: TerminalPreferences::default(),
                },
            )
            .expect("create terminal fixture profile");
        let ssh = Arc::new(SshService::new(Arc::clone(&store)));
        let confirmation_id = match ssh
            .prepare(profile.id)
            .expect("prepare terminal fixture host")
        {
            SshHostKeyStatus::ConfirmationRequired {
                confirmation_id, ..
            } => confirmation_id,
            unexpected => panic!("fixture host must require confirmation: {unexpected:?}"),
        };
        assert!(matches!(
            ssh.trust_host_key(profile.id, confirmation_id)
                .expect("trust terminal fixture host"),
            SshHostKeyStatus::Trusted { .. }
        ));

        let service = Arc::new(TerminalService::new(Arc::clone(&ssh)));
        let event_service = Arc::clone(&service);
        let (sender, receiver) = mpsc::channel();
        let events = IpcChannel::new(move |body| {
            let event = body
                .deserialize::<Value>()
                .expect("deserialize terminal event");
            if event["kind"] == "output" {
                let session_id = event["sessionId"]
                    .as_str()
                    .expect("terminal output session id")
                    .parse::<Uuid>()
                    .expect("valid terminal output session id");
                let sequence = event["sequence"]
                    .as_u64()
                    .expect("terminal output sequence");
                event_service
                    .acknowledge_output(session_id, sequence)
                    .expect("acknowledge terminal output");
            }
            sender
                .send(event)
                .map_err(|_| "terminal event receiver dropped")
        });
        let admission = OperationAdmission::new();
        let permit = admission
            .try_acquire(profile.id)
            .expect("admit terminal fixture session");
        let started = service
            .start(
                profile.id,
                Some(100),
                Some(30),
                events,
                permit.close_signal(),
            )
            .expect("start native terminal fixture session");
        assert_eq!(
            service
                .profile_id_for_session(started.session_id)
                .expect("terminal profile id"),
            profile.id
        );
        service
            .resize(started.session_id, 132, 40)
            .expect("resize native terminal fixture session");
        service
            .write(started.session_id, "terminal233-terminal-ping\n".to_owned())
            .expect("write native terminal fixture input");

        let deadline = Instant::now() + Duration::from_secs(20);
        let mut output = Vec::new();
        loop {
            let remaining = deadline
                .checked_duration_since(Instant::now())
                .expect("terminal fixture must answer before the deadline");
            let event = receiver
                .recv_timeout(remaining)
                .expect("receive terminal fixture event");
            match event["kind"].as_str() {
                Some("output") => {
                    let data = STANDARD
                        .decode(event["dataBase64"].as_str().expect("terminal output data"))
                        .expect("decode terminal output data");
                    output.extend_from_slice(&data);
                    if output
                        .windows(b"terminal233-terminal-pong".len())
                        .any(|window| window == b"terminal233-terminal-pong")
                    {
                        break;
                    }
                }
                Some("error") => panic!("native terminal fixture returned an error: {event}"),
                Some("closed") => panic!("native terminal fixture closed before its reply"),
                _ => panic!("unexpected native terminal event: {event}"),
            }
        }
        assert!(
            output
                .windows(b"terminal233-terminal-pong".len())
                .any(|window| window == b"terminal233-terminal-pong")
        );

        service
            .close(started.session_id)
            .expect("close native terminal fixture session");
        let mut saw_closed = false;
        while !saw_closed {
            let event = receiver
                .recv_timeout(Duration::from_secs(20))
                .expect("receive terminal close event");
            match event["kind"].as_str() {
                Some("output") => {}
                Some("error") => panic!("native terminal fixture returned an error: {event}"),
                Some("closed") => saw_closed = true,
                _ => panic!("unexpected native terminal close event: {event}"),
            }
        }
        assert!(matches!(
            service.write(started.session_id, "late-input".to_owned()),
            Err(NekoError::SshTerminalSessionNotFound)
        ));
    }

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
