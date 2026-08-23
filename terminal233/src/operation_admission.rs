//! Bounded admission and per-profile retirement for blocking native SSH operations.
//!
//! rdesktop's event loop is intentionally not used as the capacity limit: it is shared with
//! unrelated application work and can grow well beyond the number of SSH handshakes that this
//! process should perform concurrently.  This small gate rejects excess work immediately and
//! makes disconnect/idle expiry a one-way barrier for a profile.

use std::{
    collections::HashMap,
    sync::{
        Arc, Condvar, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

use uuid::Uuid;

pub(crate) const MAX_GLOBAL_BLOCKING_OPERATIONS: usize = 8;
pub(crate) const MAX_PROFILE_BLOCKING_OPERATIONS: usize = 3;
const MAX_GLOBAL_LIGHTWEIGHT_ACTIVITIES: usize = 256;
const MAX_PROFILE_LIGHTWEIGHT_ACTIVITIES: usize = 64;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum AdmissionError {
    Busy,
    Closing,
    Unavailable,
}

#[derive(Clone)]
pub(crate) struct OperationAdmission {
    inner: Arc<AdmissionInner>,
}

struct AdmissionInner {
    state: Mutex<AdmissionState>,
    idle: Condvar,
}

#[derive(Default)]
struct AdmissionState {
    global_blocking_active: usize,
    global_activity_active: usize,
    shutting_down: bool,
    profiles: HashMap<Uuid, ProfileAdmission>,
}

struct ProfileAdmission {
    active: usize,
    blocking_active: usize,
    activity_active: usize,
    closing: bool,
    retire_when_idle: bool,
    close_signal: ProfileCloseSignal,
}

#[derive(Clone)]
pub(crate) struct OperationPermit {
    lease: Arc<OperationLease>,
}

struct OperationLease {
    inner: Arc<AdmissionInner>,
    profile_id: Uuid,
    close_signal: ProfileCloseSignal,
    kind: PermitKind,
}

#[derive(Clone, Copy)]
enum PermitKind {
    Blocking,
    Activity,
}

/// A cloneable, process-local closing signal for work that outlives its admission call, such as
/// an SFTP transfer worker. The signal remains valid even after the admission tombstone is
/// retired, so late workers cannot mistake a removed profile entry for an open connection.
#[derive(Clone)]
pub(crate) struct ProfileCloseSignal {
    closing: Arc<AtomicBool>,
}

pub(crate) struct ProfileCloseGuard {
    inner: Arc<AdmissionInner>,
    profile_id: Uuid,
    retired: bool,
}

impl Default for ProfileAdmission {
    fn default() -> Self {
        Self {
            active: 0,
            blocking_active: 0,
            activity_active: 0,
            closing: false,
            retire_when_idle: false,
            close_signal: ProfileCloseSignal::new(),
        }
    }
}

impl ProfileCloseSignal {
    fn new() -> Self {
        Self {
            closing: Arc::new(AtomicBool::new(false)),
        }
    }

    pub(crate) fn is_closing(&self) -> bool {
        self.closing.load(Ordering::SeqCst)
    }

    /// Linearization point immediately before a remote side effect. If closing won the global
    /// sequentially-consistent order, the caller must not issue the remote operation. If this
    /// check won, the operation is considered already started and the bounded drain accounts for
    /// its admission permit.
    pub(crate) fn try_begin_remote_effect(&self) -> bool {
        !self.is_closing()
    }

    fn set_closing(&self, closing: bool) {
        self.closing.store(closing, Ordering::SeqCst);
    }
}

impl Default for OperationAdmission {
    fn default() -> Self {
        Self::new()
    }
}

impl OperationAdmission {
    pub(crate) fn new() -> Self {
        Self {
            inner: Arc::new(AdmissionInner {
                state: Mutex::new(AdmissionState::default()),
                idle: Condvar::new(),
            }),
        }
    }

    /// Reserves one blocking operation without waiting.  Immediate backpressure keeps both the
    /// async runtime and puressh connection fan-out bounded even under a hostile renderer.
    pub(crate) fn try_acquire(&self, profile_id: Uuid) -> Result<OperationPermit, AdmissionError> {
        let mut state = self
            .inner
            .state
            .lock()
            .map_err(|_| AdmissionError::Unavailable)?;
        if state.shutting_down {
            return Err(AdmissionError::Closing);
        }
        if state.global_blocking_active >= MAX_GLOBAL_BLOCKING_OPERATIONS {
            return Err(AdmissionError::Busy);
        }
        let profile = state.profiles.entry(profile_id).or_default();
        if profile.closing {
            return Err(AdmissionError::Closing);
        }
        if profile.blocking_active >= MAX_PROFILE_BLOCKING_OPERATIONS {
            return Err(AdmissionError::Busy);
        }
        profile.active = profile
            .active
            .checked_add(1)
            .ok_or(AdmissionError::Unavailable)?;
        profile.blocking_active += 1;
        let close_signal = profile.close_signal.clone();
        state.global_blocking_active += 1;
        Ok(OperationPermit {
            lease: Arc::new(OperationLease {
                inner: Arc::clone(&self.inner),
                profile_id,
                close_signal,
                kind: PermitKind::Blocking,
            }),
        })
    }

    /// Reserves a short synchronous terminal activity independently from blocking SSH capacity.
    /// These permits still participate in the profile close drain, but active SFTP/handshake work
    /// cannot starve PTY input, resize, or output acknowledgements.
    pub(crate) fn try_acquire_activity(
        &self,
        profile_id: Uuid,
    ) -> Result<OperationPermit, AdmissionError> {
        let mut state = self
            .inner
            .state
            .lock()
            .map_err(|_| AdmissionError::Unavailable)?;
        if state.shutting_down {
            return Err(AdmissionError::Closing);
        }
        if state.global_activity_active >= MAX_GLOBAL_LIGHTWEIGHT_ACTIVITIES {
            return Err(AdmissionError::Busy);
        }
        let profile = state.profiles.entry(profile_id).or_default();
        if profile.closing {
            return Err(AdmissionError::Closing);
        }
        if profile.activity_active >= MAX_PROFILE_LIGHTWEIGHT_ACTIVITIES {
            return Err(AdmissionError::Busy);
        }
        profile.active = profile
            .active
            .checked_add(1)
            .ok_or(AdmissionError::Unavailable)?;
        profile.activity_active += 1;
        let close_signal = profile.close_signal.clone();
        state.global_activity_active += 1;
        Ok(OperationPermit {
            lease: Arc::new(OperationLease {
                inner: Arc::clone(&self.inner),
                profile_id,
                close_signal,
                kind: PermitKind::Activity,
            }),
        })
    }

    /// Atomically prevents future operations for a profile. Existing permits remain valid while
    /// the caller performs a bounded drain; after `retire`, the barrier remains until the last
    /// late permit is dropped.
    pub(crate) fn begin_close(
        &self,
        profile_id: Uuid,
    ) -> Result<ProfileCloseGuard, AdmissionError> {
        let mut state = self
            .inner
            .state
            .lock()
            .map_err(|_| AdmissionError::Unavailable)?;
        if state.shutting_down {
            return Err(AdmissionError::Closing);
        }
        let profile = state.profiles.entry(profile_id).or_default();
        if profile.closing {
            return Err(AdmissionError::Closing);
        }
        profile.closing = true;
        profile.close_signal.set_closing(true);
        Ok(ProfileCloseGuard {
            inner: Arc::clone(&self.inner),
            profile_id,
            retired: false,
        })
    }

    pub(crate) fn shutdown(&self) {
        if let Ok(mut state) = self.inner.state.lock() {
            state.shutting_down = true;
            for profile in state.profiles.values_mut() {
                profile.closing = true;
                profile.retire_when_idle = true;
                profile.close_signal.set_closing(true);
            }
            self.inner.idle.notify_all();
        }
    }

    pub(crate) fn is_shutdown(&self) -> bool {
        self.inner
            .state
            .lock()
            .map(|state| state.shutting_down)
            .unwrap_or(true)
    }
}

impl OperationPermit {
    /// Used after a network resource is registered. If retirement raced with registration, the
    /// caller must close/cancel that resource before returning it to the renderer.
    pub(crate) fn profile_is_closing(&self) -> bool {
        if self.lease.close_signal.is_closing() {
            return true;
        }
        self.lease
            .inner
            .state
            .lock()
            .map(|state| {
                state.shutting_down
                    || state
                        .profiles
                        .get(&self.lease.profile_id)
                        .is_some_and(|profile| profile.closing)
            })
            .unwrap_or(true)
    }

    pub(crate) fn close_signal(&self) -> ProfileCloseSignal {
        self.lease.close_signal.clone()
    }
}

impl Drop for OperationLease {
    fn drop(&mut self) {
        if let Ok(mut state) = self.inner.state.lock() {
            match self.kind {
                PermitKind::Blocking => {
                    state.global_blocking_active = state.global_blocking_active.saturating_sub(1);
                }
                PermitKind::Activity => {
                    state.global_activity_active = state.global_activity_active.saturating_sub(1);
                }
            }
            let remove_profile = if let Some(profile) = state.profiles.get_mut(&self.profile_id) {
                profile.active = profile.active.saturating_sub(1);
                match self.kind {
                    PermitKind::Blocking => {
                        profile.blocking_active = profile.blocking_active.saturating_sub(1);
                    }
                    PermitKind::Activity => {
                        profile.activity_active = profile.activity_active.saturating_sub(1);
                    }
                }
                profile.active == 0 && (profile.retire_when_idle || !profile.closing)
            } else {
                false
            };
            if remove_profile {
                state.profiles.remove(&self.profile_id);
            }
            self.inner.idle.notify_all();
        }
    }
}

impl ProfileCloseGuard {
    /// Waits only for operations that passed admission before the closing barrier. A timeout does
    /// not reopen the profile; `retire` keeps it closed until every late permit eventually drops.
    pub(crate) fn wait_for_idle(&self, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        let Ok(mut state) = self.inner.state.lock() else {
            return false;
        };
        loop {
            let active = state
                .profiles
                .get(&self.profile_id)
                .map_or(0, |profile| profile.active);
            if active == 0 {
                return true;
            }
            let Some(remaining) = deadline.checked_duration_since(Instant::now()) else {
                return false;
            };
            let Ok((next_state, timeout_result)) = self.inner.idle.wait_timeout(state, remaining)
            else {
                return false;
            };
            state = next_state;
            if timeout_result.timed_out()
                && state
                    .profiles
                    .get(&self.profile_id)
                    .is_some_and(|profile| profile.active > 0)
            {
                return false;
            }
        }
    }

    /// Makes retirement permanent. If a bounded drain timed out, the last permit removes the
    /// tombstone; until then all new work for this profile remains rejected.
    pub(crate) fn retire(mut self) {
        if let Ok(mut state) = self.inner.state.lock() {
            let remove_profile = if let Some(profile) = state.profiles.get_mut(&self.profile_id) {
                profile.closing = true;
                profile.retire_when_idle = true;
                profile.close_signal.set_closing(true);
                profile.active == 0
            } else {
                false
            };
            if remove_profile {
                state.profiles.remove(&self.profile_id);
            }
            self.inner.idle.notify_all();
        }
        self.retired = true;
    }
}

impl Drop for ProfileCloseGuard {
    fn drop(&mut self) {
        if self.retired {
            return;
        }
        // A failed expiry claim must not strand a still-live profile in the closing state.
        if let Ok(mut state) = self.inner.state.lock() {
            let remove_profile = if let Some(profile) = state.profiles.get_mut(&self.profile_id) {
                profile.closing = false;
                profile.retire_when_idle = false;
                profile.close_signal.set_closing(false);
                profile.active == 0
            } else {
                false
            };
            if remove_profile {
                state.profiles.remove(&self.profile_id);
            }
            self.inner.idle.notify_all();
        }
    }
}

#[cfg(test)]
mod tests {
    use std::{thread, time::Instant};

    use super::*;

    #[test]
    fn admission_bounds_each_profile_and_the_process() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let profile_permits: Vec<_> = (0..MAX_PROFILE_BLOCKING_OPERATIONS)
            .map(|_| admission.try_acquire(profile).expect("profile permit"))
            .collect();
        assert!(matches!(
            admission.try_acquire(profile),
            Err(AdmissionError::Busy)
        ));

        let mut global_permits = profile_permits;
        for _ in global_permits.len()..MAX_GLOBAL_BLOCKING_OPERATIONS {
            global_permits.push(
                admission
                    .try_acquire(Uuid::new_v4())
                    .expect("global permit"),
            );
        }
        assert!(matches!(
            admission.try_acquire(Uuid::new_v4()),
            Err(AdmissionError::Busy)
        ));
        drop(global_permits);
        assert!(admission.try_acquire(profile).is_ok());
    }

    #[test]
    fn lightweight_activity_is_independent_but_still_obeys_close() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let blocking: Vec<_> = (0..MAX_PROFILE_BLOCKING_OPERATIONS)
            .map(|_| admission.try_acquire(profile).expect("blocking permit"))
            .collect();
        assert!(matches!(
            admission.try_acquire(profile),
            Err(AdmissionError::Busy)
        ));

        let activity = admission
            .try_acquire_activity(profile)
            .expect("activity remains responsive");
        let guard = admission.begin_close(profile).expect("close guard");
        assert!(activity.profile_is_closing());
        assert!(matches!(
            admission.try_acquire_activity(profile),
            Err(AdmissionError::Closing)
        ));

        drop(activity);
        drop(blocking);
        assert!(guard.wait_for_idle(Duration::from_secs(1)));
        guard.retire();
    }

    #[test]
    fn closing_barrier_waits_for_existing_work_and_rejects_new_work() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let permit = admission.try_acquire(profile).expect("permit");
        let close_signal = permit.close_signal();
        let guard = admission.begin_close(profile).expect("close guard");
        assert!(permit.profile_is_closing());
        assert!(close_signal.is_closing());
        assert!(!close_signal.try_begin_remote_effect());
        assert!(matches!(
            admission.try_acquire(profile),
            Err(AdmissionError::Closing)
        ));

        let release = thread::spawn(move || drop(permit));
        assert!(guard.wait_for_idle(Duration::from_secs(1)));
        release.join().expect("permit thread");
        guard.retire();
        assert!(admission.try_acquire(profile).is_ok());
    }

    #[test]
    fn bounded_wait_times_out_without_reopening_a_retired_profile() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let permit = admission.try_acquire(profile).expect("permit");
        let guard = admission.begin_close(profile).expect("close guard");
        let started = Instant::now();
        assert!(!guard.wait_for_idle(Duration::from_millis(15)));
        assert!(started.elapsed() < Duration::from_secs(1));
        let late_worker_signal = permit.close_signal();
        guard.retire();
        assert!(!late_worker_signal.try_begin_remote_effect());
        assert!(matches!(
            admission.try_acquire(profile),
            Err(AdmissionError::Closing)
        ));
        drop(permit);
        // The admission tombstone can now be reclaimed, but a previously spawned transfer worker
        // must keep observing the old profile as closed.
        assert!(late_worker_signal.is_closing());
        assert!(admission.try_acquire(profile).is_ok());
    }

    #[test]
    fn cloned_permit_keeps_background_work_in_the_close_drain() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let foreground = admission.try_acquire(profile).expect("permit");
        let background = foreground.clone();
        let guard = admission.begin_close(profile).expect("close guard");
        drop(foreground);

        assert!(!guard.wait_for_idle(Duration::from_millis(15)));
        drop(background);
        assert!(guard.wait_for_idle(Duration::from_secs(1)));
        guard.retire();
    }

    #[test]
    fn an_abandoned_close_guard_reopens_the_profile() {
        let admission = OperationAdmission::new();
        let profile = Uuid::new_v4();
        let permit = admission.try_acquire(profile).expect("permit");
        let close_signal = permit.close_signal();
        let guard = admission.begin_close(profile).expect("close guard");
        assert!(close_signal.is_closing());
        drop(guard);
        assert!(!close_signal.is_closing());
        drop(permit);
        assert!(admission.try_acquire(profile).is_ok());
    }
}
