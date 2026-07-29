//! In-memory SSH connections used by the standalone browser server and the simplified desktop
//! IDE.  Nothing in this module serializes a profile, credential, host-key record, or agent
//! grant.  Dropping the process, disconnecting, or an idle timeout drops and zeroizes them.

use std::{
    collections::HashMap,
    sync::Mutex,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::Serialize;
use uuid::Uuid;

use crate::{
    domain::{HostKeyVerification, KnownHostRecord, KnownHosts, SshProfile, SshProfileInput},
    error::{Result, VaultError},
    ssh::SshProfileStore,
};

pub const CONNECTION_IDLE_TTL: Duration = Duration::from_secs(30 * 60);
const AGENT_GRANT_TTL: Duration = Duration::from_secs(5 * 60);

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct EphemeralConnectionSummary {
    pub id: Uuid,
    pub label: String,
    pub host: String,
    pub port: u16,
    pub username: String,
    pub tags: Vec<String>,
    pub expires_in_seconds: u64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentConnectionGrant {
    /// Opaque, one-time value. It has no credential material and expires in five minutes.
    pub grant: Uuid,
    pub expires_in_seconds: u64,
}

pub struct EphemeralConnectionStore {
    inner: Mutex<StoreState>,
}

#[derive(Default)]
struct StoreState {
    connections: HashMap<Uuid, Connection>,
}

struct Connection {
    owner: Uuid,
    profile: SshProfile,
    known_hosts: KnownHosts,
    expires_at: Instant,
    agent_grants: HashMap<Uuid, Instant>,
    agent_attached_until: Option<Instant>,
}

impl Default for EphemeralConnectionStore {
    fn default() -> Self {
        Self::new()
    }
}

impl EphemeralConnectionStore {
    pub fn new() -> Self {
        Self {
            inner: Mutex::new(StoreState::default()),
        }
    }

    pub fn create(
        &self,
        owner: Uuid,
        input: SshProfileInput,
    ) -> Result<EphemeralConnectionSummary> {
        let profile = SshProfile::from_input(input, now_unix_ms())?;
        let summary = summary_for(&profile, CONNECTION_IDLE_TTL);
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        state.connections.insert(
            profile.id,
            Connection {
                owner,
                profile,
                known_hosts: KnownHosts::default(),
                expires_at: Instant::now() + CONNECTION_IDLE_TTL,
                agent_grants: HashMap::new(),
                agent_attached_until: None,
            },
        );
        Ok(summary)
    }

    pub fn browser_connection(&self, owner: Uuid, id: Uuid) -> Result<EphemeralConnectionSummary> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get_mut(&id)
            .filter(|connection| connection.owner == owner)
            .ok_or(VaultError::ProfileNotFound)?;
        connection.touch();
        Ok(summary_for(
            &connection.profile,
            remaining(connection.expires_at),
        ))
    }

    pub fn remove_browser_connection(&self, owner: Uuid, id: Uuid) -> Result<()> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get(&id)
            .ok_or(VaultError::ProfileNotFound)?;
        if connection.owner != owner {
            return Err(VaultError::ProfileNotFound);
        }
        state.connections.remove(&id);
        Ok(())
    }

    pub fn create_agent_grant(&self, owner: Uuid, id: Uuid) -> Result<AgentConnectionGrant> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get_mut(&id)
            .filter(|connection| connection.owner == owner)
            .ok_or(VaultError::ProfileNotFound)?;
        connection.touch();
        let grant = Uuid::new_v4();
        connection
            .agent_grants
            .insert(grant, Instant::now() + AGENT_GRANT_TTL);
        Ok(AgentConnectionGrant {
            grant,
            expires_in_seconds: AGENT_GRANT_TTL.as_secs(),
        })
    }

    /// Turns a browser-created one-time grant into a short-lived MCP attachment.  The agent
    /// receives only the connection UUID, never its password or private key.
    pub fn attach_agent(&self, grant: Uuid) -> Result<EphemeralConnectionSummary> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let now = Instant::now();
        let connection = state
            .connections
            .values_mut()
            .find(|connection| {
                connection
                    .agent_grants
                    .get(&grant)
                    .is_some_and(|expires| *expires > now)
            })
            .ok_or(VaultError::ProfileNotFound)?;
        connection.agent_grants.remove(&grant);
        connection.agent_attached_until = Some(now + CONNECTION_IDLE_TTL);
        connection.touch();
        Ok(summary_for(
            &connection.profile,
            remaining(connection.expires_at),
        ))
    }

    pub fn agent_connection(&self, id: Uuid) -> Result<EphemeralConnectionSummary> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let now = Instant::now();
        let connection = state
            .connections
            .get_mut(&id)
            .filter(|connection| {
                connection
                    .agent_attached_until
                    .is_some_and(|expires| expires > now)
            })
            .ok_or(VaultError::ProfileNotFound)?;
        connection.agent_attached_until = Some(now + CONNECTION_IDLE_TTL);
        connection.touch();
        Ok(summary_for(
            &connection.profile,
            remaining(connection.expires_at),
        ))
    }

    pub fn remove_agent_connection(&self, id: Uuid) -> Result<()> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get(&id)
            .ok_or(VaultError::ProfileNotFound)?;
        if connection.agent_attached_until.is_none() {
            return Err(VaultError::ProfileNotFound);
        }
        state.connections.remove(&id);
        Ok(())
    }

    pub fn browser_list(&self, owner: Uuid) -> Result<Vec<EphemeralConnectionSummary>> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let now = Instant::now();
        let mut result: Vec<_> = state
            .connections
            .values_mut()
            .filter(|connection| connection.owner == owner)
            .map(|connection| {
                connection.touch();
                summary_for(&connection.profile, remaining(connection.expires_at))
            })
            .collect();
        // Silence the unused local in optimized builds while making the common timestamp explicit.
        let _ = now;
        result.sort_by(|left, right| left.label.to_lowercase().cmp(&right.label.to_lowercase()));
        Ok(result)
    }

    /// Removes idle profiles and returns their IDs so the owning terminal registry can close
    /// shells before an in-memory credential disappears.
    pub fn purge_expired(&self) -> Result<Vec<Uuid>> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        Ok(state.purge_expired())
    }
}

impl SshProfileStore for EphemeralConnectionStore {
    fn ssh_profile_snapshot(&self, profile_id: Uuid) -> Result<SshProfile> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get_mut(&profile_id)
            .ok_or(VaultError::ProfileNotFound)?;
        connection.touch();
        Ok(connection.profile.clone())
    }

    fn verify_host_key(
        &self,
        profile_id: Uuid,
        host: &str,
        port: u16,
        key_algorithm: &str,
        public_key: &[u8],
    ) -> Result<HostKeyVerification> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get_mut(&profile_id)
            .ok_or(VaultError::ProfileNotFound)?;
        connection.touch();
        Ok(connection
            .known_hosts
            .verify(host, port, key_algorithm, public_key))
    }

    fn trust_host_key(
        &self,
        profile_id: Uuid,
        host: String,
        port: u16,
        key_algorithm: String,
        public_key: &[u8],
    ) -> Result<()> {
        let mut state = self.inner.lock().map_err(|_| VaultError::LockPoisoned)?;
        let _ = state.purge_expired();
        let connection = state
            .connections
            .get_mut(&profile_id)
            .ok_or(VaultError::ProfileNotFound)?;
        let record =
            KnownHostRecord::from_presented(host, port, key_algorithm, public_key, now_unix_ms())?;
        connection.known_hosts.trust(record);
        connection.touch();
        Ok(())
    }
}

impl StoreState {
    fn purge_expired(&mut self) -> Vec<Uuid> {
        let now = Instant::now();
        let expired: Vec<_> = self
            .connections
            .iter()
            .filter_map(|(id, connection)| (connection.expires_at <= now).then_some(*id))
            .collect();
        self.connections.retain(|_, connection| {
            connection.agent_grants.retain(|_, expires| *expires > now);
            connection.agent_attached_until = connection
                .agent_attached_until
                .filter(|expires| *expires > now);
            connection.expires_at > now
        });
        expired
    }
}

impl Connection {
    fn touch(&mut self) {
        self.expires_at = Instant::now() + CONNECTION_IDLE_TTL;
    }
}

fn remaining(expires_at: Instant) -> Duration {
    expires_at.saturating_duration_since(Instant::now())
}

fn summary_for(profile: &SshProfile, expires_in: Duration) -> EphemeralConnectionSummary {
    EphemeralConnectionSummary {
        id: profile.id,
        label: profile.label.clone(),
        host: profile.host.clone(),
        port: profile.port,
        username: profile.username.clone(),
        tags: profile.tags.clone(),
        expires_in_seconds: expires_in.as_secs(),
    }
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
    use super::*;
    use crate::domain::{ProfileAuthenticationInput, TerminalPreferences};

    fn input() -> SshProfileInput {
        SshProfileInput {
            label: "temporary".to_owned(),
            host: "example.test".to_owned(),
            port: 22,
            username: "root".to_owned(),
            authentication: ProfileAuthenticationInput::Password {
                password: "never-persisted".to_owned(),
            },
            tags: Vec::new(),
            terminal: TerminalPreferences::default(),
        }
    }

    #[test]
    fn grant_is_one_time_and_does_not_make_the_profile_public() {
        let store = EphemeralConnectionStore::new();
        let owner = Uuid::new_v4();
        let connection = store.create(owner, input()).expect("connection");
        assert!(store.agent_connection(connection.id).is_err());
        let grant = store
            .create_agent_grant(owner, connection.id)
            .expect("browser grant");
        let attached = store.attach_agent(grant.grant).expect("attach");
        assert_eq!(attached.id, connection.id);
        assert!(store.attach_agent(grant.grant).is_err());
        assert!(store.agent_connection(connection.id).is_ok());
    }
}
