//! Encrypted cloud-sync artifacts and provider-neutral transfer plans.
//!
//! Credentials are kept in the encrypted vault.  Transfer receipts and plans intentionally carry
//! only public remote metadata and an encrypted-bundle fingerprint.

use std::{
    fs,
    path::{Path, PathBuf},
    time::Duration,
};

use base64::{Engine as _, engine::general_purpose::STANDARD};
use git2::{
    AutotagOption, Cred, ErrorCode, FetchOptions, Oid, PushOptions, RemoteCallbacks,
    RemoteRedirect, Repository, RepositoryInitOptions, Signature,
};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use url::Url;
use uuid::Uuid;
use zeroize::Zeroizing;

use crate::{
    crypto::VAULT_FORMAT,
    domain::{GitProvider, StoredSecret, SyncConfiguration, SyncKind, normalize_webdav_endpoint},
    error::{Result, VaultError},
    vault::VaultEnvelope,
};

pub const SYNC_BUNDLE_CONTENT_TYPE: &str = "application/vnd.neko233.vault+json";
/// Upper bound for a backup read from a cloud endpoint before its authenticated envelope is parsed.
pub const MAX_SYNC_BUNDLE_BYTES: usize = 16 * 1024 * 1024;
const WEBDAV_REQUEST_TIMEOUT: Duration = Duration::from_secs(30);

/// A portable copy of an encrypted vault envelope. It contains no decrypted vault data.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct EncryptedSyncBundle {
    pub format: String,
    pub exported_at_unix_ms: u64,
    pub vault: VaultEnvelope,
}

impl EncryptedSyncBundle {
    pub fn from_envelope(envelope: &VaultEnvelope, exported_at_unix_ms: u64) -> Self {
        Self {
            format: VAULT_FORMAT.to_owned(),
            exported_at_unix_ms,
            vault: envelope.clone(),
        }
    }

    pub fn to_bytes(&self) -> Result<Vec<u8>> {
        self.validate()?;
        serde_json::to_vec(self).map_err(VaultError::Serialization)
    }

    pub fn from_bytes(bytes: &[u8]) -> Result<Self> {
        let bundle: Self = serde_json::from_slice(bytes).map_err(VaultError::Serialization)?;
        bundle.validate()?;
        Ok(bundle)
    }

    pub fn encrypted_sha256(&self) -> Result<String> {
        Ok(format!("sha256:{:x}", Sha256::digest(self.to_bytes()?)))
    }

    pub fn validate(&self) -> Result<()> {
        if self.format != VAULT_FORMAT {
            return Err(VaultError::UnsupportedFormat);
        }
        self.vault.validate_structure()
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum SyncOperation {
    Upload,
    Download,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CredentialRequirement {
    VaultWebDavCredential,
    VaultGitToken,
    /// Present in configuration plans for compatibility. The executable transport intentionally
    /// rejects SSH Git remotes until it has a host-key verifier bound to the encrypted vault.
    ExternalSshAgent,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(tag = "adapter", rename_all = "snake_case")]
pub enum SyncTransferPlan {
    WebDav {
        operation: SyncOperation,
        url: String,
        content_type: String,
        encrypted_bundle_sha256: String,
        credential_requirement: CredentialRequirement,
    },
    Git {
        operation: SyncOperation,
        provider: GitProvider,
        remote_url: String,
        branch: String,
        artifact_path: String,
        encrypted_bundle_sha256: String,
        credential_requirement: CredentialRequirement,
    },
}

/// Non-sensitive confirmation returned after an actual cloud transfer.
#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SyncTransferReceipt {
    pub kind: SyncKind,
    pub operation: SyncOperation,
    pub encrypted_bundle_sha256: String,
    pub bytes: usize,
}

/// A server-provided opaque version validator. It remains in the encrypted vault and is never
/// returned in a UI receipt, so a later upload can use a conditional write rather than silently
/// replacing another device's WebDAV backup.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum WebDavWriteCondition {
    CreateOnly,
    Match(String),
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct WebDavUploadResult {
    pub receipt: SyncTransferReceipt,
    pub remote_etag: String,
}

/// Private request material. Do not derive Debug, Serialize, or Clone: authorization is secret.
struct WebDavRequest {
    url: String,
    authorization: Zeroizing<String>,
    content_type: String,
}

trait WebDavTransport {
    fn put(
        &self,
        request: &WebDavRequest,
        encrypted_bundle: &[u8],
        condition: &WebDavWriteCondition,
    ) -> Result<String>;
    fn get(&self, request: &WebDavRequest) -> Result<Vec<u8>>;
}

/// HTTPS-only, redirect-free transport. A redirect is not followed so an Authorization header
/// cannot be replayed to a URL different from the one the user configured.
struct UreqWebDavTransport;

impl UreqWebDavTransport {
    fn agent() -> ureq::Agent {
        ureq::Agent::config_builder()
            .https_only(true)
            .max_redirects(0)
            .http_status_as_error(false)
            .timeout_global(Some(WEBDAV_REQUEST_TIMEOUT))
            .build()
            .into()
    }
}

impl WebDavTransport for UreqWebDavTransport {
    fn put(
        &self,
        request: &WebDavRequest,
        encrypted_bundle: &[u8],
        condition: &WebDavWriteCondition,
    ) -> Result<String> {
        let request_builder = Self::agent()
            .put(&request.url)
            .header("Authorization", request.authorization.as_str())
            .header("Content-Type", &request.content_type);
        let request_builder = match condition {
            WebDavWriteCondition::CreateOnly => request_builder.header("If-None-Match", "*"),
            WebDavWriteCondition::Match(etag) => request_builder.header("If-Match", etag),
        };
        let response = request_builder
            .send(encrypted_bundle)
            .map_err(|_| VaultError::SyncTransportFailed)?;
        if matches!(response.status().as_u16(), 409 | 412) {
            return Err(VaultError::SyncConflict);
        }
        if !response.status().is_success() {
            return Err(VaultError::SyncTransportFailed);
        }
        response
            .headers()
            .get("etag")
            .and_then(|etag| etag.to_str().ok())
            .ok_or(VaultError::SyncVersionUnavailable)
            .and_then(validate_webdav_etag)
    }

    fn get(&self, request: &WebDavRequest) -> Result<Vec<u8>> {
        let mut response = Self::agent()
            .get(&request.url)
            .header("Authorization", request.authorization.as_str())
            .call()
            .map_err(|_| VaultError::SyncTransportFailed)?;
        if !response.status().is_success() {
            return Err(VaultError::SyncTransportFailed);
        }
        response
            .body_mut()
            .with_config()
            .limit(MAX_SYNC_BUNDLE_BYTES as u64)
            .read_to_vec()
            .map_err(|_| VaultError::SyncTransportFailed)
    }
}

struct WebDavSyncExecutor<T> {
    transport: T,
}

impl<T: WebDavTransport> WebDavSyncExecutor<T> {
    fn new(transport: T) -> Self {
        Self { transport }
    }

    fn upload(
        &self,
        configuration: &SyncConfiguration,
        bundle: &EncryptedSyncBundle,
        expected_remote_etag: Option<&str>,
    ) -> Result<WebDavUploadResult> {
        let encrypted_bytes = checked_bundle_bytes(bundle)?;
        let request = webdav_request(configuration)?;
        let condition = match expected_remote_etag {
            Some(etag) => WebDavWriteCondition::Match(validate_webdav_etag(etag)?),
            None => WebDavWriteCondition::CreateOnly,
        };
        let remote_etag = self.transport.put(&request, &encrypted_bytes, &condition)?;
        Ok(WebDavUploadResult {
            receipt: SyncTransferReceipt {
                kind: SyncKind::WebDav,
                operation: SyncOperation::Upload,
                encrypted_bundle_sha256: bundle.encrypted_sha256()?,
                bytes: encrypted_bytes.len(),
            },
            remote_etag,
        })
    }

    fn download(&self, configuration: &SyncConfiguration) -> Result<EncryptedSyncBundle> {
        let request = webdav_request(configuration)?;
        let bytes = self.transport.get(&request)?;
        if bytes.len() > MAX_SYNC_BUNDLE_BYTES {
            return Err(VaultError::SyncBundleTooLarge);
        }
        EncryptedSyncBundle::from_bytes(&bytes)
    }
}

fn checked_bundle_bytes(bundle: &EncryptedSyncBundle) -> Result<Vec<u8>> {
    let bytes = bundle.to_bytes()?;
    if bytes.len() > MAX_SYNC_BUNDLE_BYTES {
        return Err(VaultError::SyncBundleTooLarge);
    }
    Ok(bytes)
}

pub(crate) fn validate_webdav_etag(value: &str) -> Result<String> {
    if value.is_empty()
        || value.len() > 1_024
        || value == "*"
        || value.bytes().any(|byte| !(0x20..=0x7e).contains(&byte))
    {
        return Err(VaultError::SyncVersionUnavailable);
    }
    Ok(value.to_owned())
}

fn webdav_request(configuration: &SyncConfiguration) -> Result<WebDavRequest> {
    configuration.validate()?;
    let SyncConfiguration::WebDav {
        username, secret, ..
    } = configuration
    else {
        return Err(VaultError::SyncNotWebDav);
    };
    let url = WebDavAdapter::from_configuration(configuration)?.artifact_url()?;
    let authorization = secret.with_exposed(|secret| {
        if let Some(username) = username {
            let user_and_secret = Zeroizing::new(format!("{username}:{secret}"));
            let encoded = Zeroizing::new(STANDARD.encode(user_and_secret.as_bytes()));
            Zeroizing::new(format!("Basic {}", encoded.as_str()))
        } else {
            Zeroizing::new(format!("Bearer {secret}"))
        }
    });
    Ok(WebDavRequest {
        url,
        authorization,
        content_type: SYNC_BUNDLE_CONTENT_TYPE.to_owned(),
    })
}

pub(crate) fn upload_webdav_bundle(
    configuration: &SyncConfiguration,
    bundle: &EncryptedSyncBundle,
    expected_remote_etag: Option<&str>,
) -> Result<WebDavUploadResult> {
    WebDavSyncExecutor::new(UreqWebDavTransport).upload(configuration, bundle, expected_remote_etag)
}

/// Downloads an encrypted WebDAV bundle without importing it.
pub fn download_webdav_bundle(configuration: &SyncConfiguration) -> Result<EncryptedSyncBundle> {
    WebDavSyncExecutor::new(UreqWebDavTransport).download(configuration)
}

const GIT_ARTIFACT_PATH: &str = ".nekossh/vault.bundle";
const GIT_REMOTE_REF: &str = "refs/nekossh/remote-backup";
const GIT_COMMIT_MESSAGE: &str = "NekoSSH encrypted vault backup";

/// HTTPS origin that a Git credential callback is allowed to authenticate. This prevents a
/// redirect or malicious URL from receiving a token intended for the configured host.
#[derive(Clone, PartialEq, Eq)]
struct HttpsOrigin {
    host: String,
    port: u16,
}

impl HttpsOrigin {
    fn from_remote(remote: &str) -> Result<Self> {
        let parsed =
            Url::parse(remote).map_err(|_| VaultError::InvalidSync("Git remote is not a URL"))?;
        if parsed.scheme() != "https"
            || !parsed.username().is_empty()
            || parsed.password().is_some()
            || parsed.query().is_some()
            || parsed.fragment().is_some()
        {
            return Err(VaultError::GitAuthenticationUnsupported);
        }
        let host = parsed
            .host_str()
            .ok_or(VaultError::GitAuthenticationUnsupported)?
            .to_ascii_lowercase();
        let port = parsed
            .port_or_known_default()
            .ok_or(VaultError::GitAuthenticationUnsupported)?;
        Ok(Self { host, port })
    }

    fn matches_callback_url(&self, candidate: &str) -> bool {
        let Ok(url) = Url::parse(candidate) else {
            return false;
        };
        url.scheme() == "https"
            && url.username().is_empty()
            && url.password().is_none()
            && url
                .host_str()
                .is_some_and(|host| host.eq_ignore_ascii_case(&self.host))
            && url.port_or_known_default() == Some(self.port)
    }
}

/// Private and deliberately non-serializable request state. The access token never leaves this
/// process: libgit2 consumes it through an in-memory credential callback.
struct GitTransferRequest {
    remote_url: String,
    branch: String,
    username: &'static str,
    token: StoredSecret,
    origin: HttpsOrigin,
}

impl GitTransferRequest {
    fn remote_callbacks(&self) -> RemoteCallbacks<'_> {
        let mut callbacks = RemoteCallbacks::new();
        callbacks.credentials(move |callback_url, _username_from_url, _allowed| {
            if !self.origin.matches_callback_url(callback_url) {
                return Err(git2::Error::from_str(
                    "refusing Git credentials for another origin",
                ));
            }
            self.token
                .with_exposed(|token| Cred::userpass_plaintext(self.username, token))
        });
        // No certificate callback is installed. libgit2's built-in TLS validation therefore
        // rejects invalid certificates rather than allowing the desktop UI to override trust.
        callbacks
    }
}

fn git_transfer_request(configuration: &SyncConfiguration) -> Result<GitTransferRequest> {
    configuration.validate()?;
    let SyncConfiguration::Git {
        provider,
        remote_url,
        branch,
        access_token,
    } = configuration
    else {
        return Err(VaultError::SyncNotGit);
    };
    if !remote_url.starts_with("https://") {
        // Do not expose an SSH-agent path until SSH host-key verification can reuse the vault's
        // explicit known-host trust model. A clear failure is safer than libgit2 default behavior.
        return Err(VaultError::GitAuthenticationUnsupported);
    }
    let token = access_token
        .as_ref()
        .ok_or(VaultError::GitAuthenticationUnsupported)?
        .clone();
    Ok(GitTransferRequest {
        remote_url: remote_url.clone(),
        branch: branch.clone(),
        username: match provider {
            GitProvider::Github => "x-access-token",
            GitProvider::Gitea | GitProvider::Generic => "oauth2",
        },
        token,
        origin: HttpsOrigin::from_remote(remote_url)?,
    })
}

/// A bare temporary Git repository. It stores only encrypted Git objects and is deleted when the
/// operation ends. External templates are disabled so no user hook/template is ever executed.
struct GitWorkspace {
    root: PathBuf,
}

impl GitWorkspace {
    fn new() -> Result<Self> {
        let root = std::env::temp_dir().join(format!("nekossh-git-sync-{}", Uuid::new_v4()));
        fs::create_dir(&root).map_err(|_| VaultError::SyncTransportFailed)?;
        Ok(Self { root })
    }
}

impl Drop for GitWorkspace {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

/// libgit2 avoids a `git.exe` child process entirely on Windows and other platforms. In
/// particular, HTTPS token material is neither an argv value, an environment variable, nor part
/// of a URL, Git config, sync plan, receipt, or log.
struct Git2Repository {
    // Keep this before `workspace`: Rust drops fields in declaration order, releasing libgit2's
    // file handles before trying to remove the temporary repository on Windows.
    repository: Repository,
    request: GitTransferRequest,
    _workspace: GitWorkspace,
    remote_parent: Option<Oid>,
    pending_commit: Option<Oid>,
    /// A local unauthenticated `file://` remote is used only in the real libgit2 test below.
    /// This field is compiled out of production, where every transfer keeps HTTPS origin binding.
    #[cfg(test)]
    test_allow_unauthenticated_file_remote: bool,
}

impl Git2Repository {
    fn new(configuration: &SyncConfiguration) -> Result<Self> {
        let request = git_transfer_request(configuration)?;
        let workspace = GitWorkspace::new()?;
        let mut options = RepositoryInitOptions::new();
        options
            .bare(true)
            .no_reinit(true)
            .external_template(false)
            .initial_head("main");
        let repository = Repository::init_opts(&workspace.root, &options)
            .map_err(|_| VaultError::SyncTransportFailed)?;
        Ok(Self {
            repository,
            request,
            _workspace: workspace,
            remote_parent: None,
            pending_commit: None,
            #[cfg(test)]
            test_allow_unauthenticated_file_remote: false,
        })
    }

    fn remote(&self) -> Result<git2::Remote<'_>> {
        self.repository
            .remote_anonymous(&self.request.remote_url)
            .map_err(|_| VaultError::SyncTransportFailed)
    }

    fn fetch_options(&self) -> FetchOptions<'_> {
        let mut options = FetchOptions::new();
        options
            .remote_callbacks(self.remote_callbacks())
            .download_tags(AutotagOption::None)
            .follow_redirects(RemoteRedirect::None);
        // libgit2's local file transport does not implement shallow fetch. The only test that
        // uses it exercises the same tree/push/read path without a network account; production
        // HTTPS transfers retain the one-commit download bound.
        let use_shallow_fetch = {
            #[cfg(test)]
            {
                !self.test_allow_unauthenticated_file_remote
            }
            #[cfg(not(test))]
            {
                true
            }
        };
        if use_shallow_fetch {
            options.depth(1);
        }
        options
    }

    fn remote_callbacks(&self) -> RemoteCallbacks<'_> {
        #[cfg(test)]
        if self.test_allow_unauthenticated_file_remote {
            return RemoteCallbacks::new();
        }
        self.request.remote_callbacks()
    }
}

trait GitRepository {
    /// Fetches the configured branch if it exists and records it as the non-force push parent.
    /// An empty remote is a normal first-backup state, not an error.
    fn fetch_branch_if_present(&mut self) -> Result<bool>;
    fn commit_bundle(&mut self, encrypted_bundle: &[u8]) -> Result<()>;
    /// Returns false when a remote rejects the non-force push; no retry or overwrite is allowed.
    fn push_new_commit(&mut self) -> Result<bool>;
    fn read_bundle(&mut self) -> Result<Vec<u8>>;
}

impl GitRepository for Git2Repository {
    fn fetch_branch_if_present(&mut self) -> Result<bool> {
        let refspec = format!("+refs/heads/{}:{GIT_REMOTE_REF}", self.request.branch);
        {
            let mut remote = self.remote()?;
            let mut options = self.fetch_options();
            if let Err(error) = remote.fetch(&[refspec.as_str()], Some(&mut options), None) {
                // A freshly created bare repository legitimately has no branch to fetch. Avoid
                // `Remote::list()`: git2 0.20's wrapper dereferences a null advertisement pointer
                // for that valid empty-ref response in debug builds.
                return if error.code() == ErrorCode::NotFound {
                    Ok(false)
                } else {
                    Err(VaultError::SyncTransportFailed)
                };
            }
        }
        let reference = match self.repository.find_reference(GIT_REMOTE_REF) {
            Ok(reference) => reference,
            Err(error) if error.code() == ErrorCode::NotFound => return Ok(false),
            Err(_) => return Err(VaultError::SyncTransportFailed),
        };
        let target = reference.target().ok_or(VaultError::SyncTransportFailed)?;
        self.repository
            .find_commit(target)
            .map_err(|_| VaultError::SyncTransportFailed)?;
        self.remote_parent = Some(target);
        Ok(true)
    }

    fn commit_bundle(&mut self, encrypted_bundle: &[u8]) -> Result<()> {
        let blob = self
            .repository
            .blob(encrypted_bundle)
            .map_err(|_| VaultError::SyncTransportFailed)?;
        let parent = self
            .remote_parent
            .map(|oid| self.repository.find_commit(oid))
            .transpose()
            .map_err(|_| VaultError::SyncTransportFailed)?;
        let base_tree = parent
            .as_ref()
            .map(|commit| commit.tree())
            .transpose()
            .map_err(|_| VaultError::SyncTransportFailed)?;
        let tree_id = build_tree_with_bundle(&self.repository, base_tree.as_ref(), blob)?;
        let tree = self
            .repository
            .find_tree(tree_id)
            .map_err(|_| VaultError::SyncTransportFailed)?;
        let signature = Signature::now("NekoSSH Backup", "backup@localhost")
            .map_err(|_| VaultError::SyncTransportFailed)?;
        let commit = match parent.as_ref() {
            Some(parent) => self.repository.commit(
                None,
                &signature,
                &signature,
                GIT_COMMIT_MESSAGE,
                &tree,
                &[parent],
            ),
            None => {
                self.repository
                    .commit(None, &signature, &signature, GIT_COMMIT_MESSAGE, &tree, &[])
            }
        }
        .map_err(|_| VaultError::SyncTransportFailed)?;
        self.pending_commit = Some(commit);
        Ok(())
    }

    fn push_new_commit(&mut self) -> Result<bool> {
        let commit = self.pending_commit.ok_or(VaultError::SyncTransportFailed)?;
        let refspec = format!("{commit}:refs/heads/{}", self.request.branch);
        let mut rejected = false;
        let result = {
            let mut remote = self.remote()?;
            let mut callbacks = self.remote_callbacks();
            callbacks.push_update_reference(|_reference, status| {
                if status.is_some() {
                    rejected = true;
                }
                Ok(())
            });
            let mut options = PushOptions::new();
            options
                .remote_callbacks(callbacks)
                .follow_redirects(RemoteRedirect::None);
            remote.push(&[refspec.as_str()], Some(&mut options))
        };
        // No force refspec / force option is supplied. A concurrent remote commit is left intact.
        Ok(result.is_ok() && !rejected)
    }

    fn read_bundle(&mut self) -> Result<Vec<u8>> {
        let parent = self
            .remote_parent
            .ok_or(VaultError::SyncRemoteBundleMissing)?;
        let commit = self
            .repository
            .find_commit(parent)
            .map_err(|_| VaultError::SyncRemoteBundleMissing)?;
        let tree = commit
            .tree()
            .map_err(|_| VaultError::SyncRemoteBundleMissing)?;
        let entry = tree
            .get_path(Path::new(GIT_ARTIFACT_PATH))
            .map_err(|_| VaultError::SyncRemoteBundleMissing)?;
        let blob = self
            .repository
            .find_blob(entry.id())
            .map_err(|_| VaultError::SyncRemoteBundleMissing)?;
        if blob.content().len() > MAX_SYNC_BUNDLE_BYTES {
            return Err(VaultError::SyncBundleTooLarge);
        }
        Ok(blob.content().to_vec())
    }
}

fn build_tree_with_bundle(
    repository: &Repository,
    base_tree: Option<&git2::Tree<'_>>,
    bundle_blob: Oid,
) -> Result<Oid> {
    let existing_neko_tree = base_tree
        .and_then(|tree| tree.get_name(".nekossh"))
        .and_then(|entry| repository.find_tree(entry.id()).ok());
    let mut neko_builder = repository
        .treebuilder(existing_neko_tree.as_ref())
        .map_err(|_| VaultError::SyncTransportFailed)?;
    neko_builder
        .insert("vault.bundle", bundle_blob, 0o100644)
        .map_err(|_| VaultError::SyncTransportFailed)?;
    let neko_tree = neko_builder
        .write()
        .map_err(|_| VaultError::SyncTransportFailed)?;
    let mut root_builder = repository
        .treebuilder(base_tree)
        .map_err(|_| VaultError::SyncTransportFailed)?;
    root_builder
        .insert(".nekossh", neko_tree, 0o040000)
        .map_err(|_| VaultError::SyncTransportFailed)?;
    root_builder
        .write()
        .map_err(|_| VaultError::SyncTransportFailed)
}

struct GitSyncExecutor<R> {
    repository: R,
}

impl<R: GitRepository> GitSyncExecutor<R> {
    fn new(repository: R) -> Self {
        Self { repository }
    }

    fn upload(&mut self, bundle: &EncryptedSyncBundle) -> Result<SyncTransferReceipt> {
        let bytes = checked_bundle_bytes(bundle)?;
        let _ = self.repository.fetch_branch_if_present()?;
        self.repository.commit_bundle(&bytes)?;
        if !self.repository.push_new_commit()? {
            return Err(VaultError::SyncGitPushRejected);
        }
        Ok(SyncTransferReceipt {
            kind: SyncKind::Git,
            operation: SyncOperation::Upload,
            encrypted_bundle_sha256: bundle.encrypted_sha256()?,
            bytes: bytes.len(),
        })
    }

    fn download(&mut self) -> Result<EncryptedSyncBundle> {
        if !self.repository.fetch_branch_if_present()? {
            return Err(VaultError::SyncRemoteBundleMissing);
        }
        let bytes = self.repository.read_bundle()?;
        if bytes.len() > MAX_SYNC_BUNDLE_BYTES {
            return Err(VaultError::SyncBundleTooLarge);
        }
        EncryptedSyncBundle::from_bytes(&bytes)
    }
}

/// Backs up only an already-encrypted vault envelope using libgit2. It supports HTTPS token
/// remotes (GitHub, Gitea and compatible providers); Git SSH is intentionally a clear safe failure
/// until explicit host-key verification is integrated with NekoSSH known-hosts.
pub fn upload_git_bundle(
    configuration: &SyncConfiguration,
    bundle: &EncryptedSyncBundle,
) -> Result<SyncTransferReceipt> {
    let repository = Git2Repository::new(configuration)?;
    GitSyncExecutor::new(repository).upload(bundle)
}

/// Downloads the encrypted Git artifact only; import/replacement remains a separate explicit UI
/// action that requires the local vault to be locked.
pub fn download_git_bundle(configuration: &SyncConfiguration) -> Result<EncryptedSyncBundle> {
    let repository = Git2Repository::new(configuration)?;
    GitSyncExecutor::new(repository).download()
}

/// Planning boundary. Adapters never receive the actual WebDAV secret or Git token.
pub trait SyncAdapter {
    fn upload_plan(&self, bundle: &EncryptedSyncBundle) -> Result<SyncTransferPlan>;
    fn download_plan(&self, bundle_fingerprint: String) -> Result<SyncTransferPlan>;
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WebDavAdapter {
    endpoint: Url,
    remote_path: String,
}

impl WebDavAdapter {
    pub fn from_configuration(configuration: &SyncConfiguration) -> Result<Self> {
        configuration.validate()?;
        let SyncConfiguration::WebDav {
            endpoint,
            remote_path,
            ..
        } = configuration
        else {
            return Err(VaultError::InvalidSync(
                "WebDAV adapter needs a WebDAV configuration",
            ));
        };
        let endpoint = Url::parse(&normalize_webdav_endpoint(endpoint)?)
            .map_err(|_| VaultError::InvalidSync("WebDAV endpoint is not a URL"))?;
        Ok(Self {
            endpoint,
            remote_path: remote_path.clone(),
        })
    }

    fn artifact_url(&self) -> Result<String> {
        self.endpoint
            .join(&self.remote_path)
            .map(|url| url.to_string())
            .map_err(|_| VaultError::InvalidSync("unable to construct WebDAV artifact URL"))
    }
}

impl SyncAdapter for WebDavAdapter {
    fn upload_plan(&self, bundle: &EncryptedSyncBundle) -> Result<SyncTransferPlan> {
        Ok(SyncTransferPlan::WebDav {
            operation: SyncOperation::Upload,
            url: self.artifact_url()?,
            content_type: SYNC_BUNDLE_CONTENT_TYPE.to_owned(),
            encrypted_bundle_sha256: bundle.encrypted_sha256()?,
            credential_requirement: CredentialRequirement::VaultWebDavCredential,
        })
    }

    fn download_plan(&self, bundle_fingerprint: String) -> Result<SyncTransferPlan> {
        Ok(SyncTransferPlan::WebDav {
            operation: SyncOperation::Download,
            url: self.artifact_url()?,
            content_type: SYNC_BUNDLE_CONTENT_TYPE.to_owned(),
            encrypted_bundle_sha256: bundle_fingerprint,
            credential_requirement: CredentialRequirement::VaultWebDavCredential,
        })
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct GitAdapter {
    provider: GitProvider,
    remote_url: String,
    branch: String,
    uses_vault_token: bool,
}

impl GitAdapter {
    pub fn from_configuration(configuration: &SyncConfiguration) -> Result<Self> {
        configuration.validate()?;
        let SyncConfiguration::Git {
            provider,
            remote_url,
            branch,
            access_token,
        } = configuration
        else {
            return Err(VaultError::InvalidSync(
                "Git adapter needs a Git configuration",
            ));
        };
        Ok(Self {
            provider: *provider,
            remote_url: remote_url.clone(),
            branch: branch.clone(),
            uses_vault_token: access_token.is_some(),
        })
    }

    fn credential_requirement(&self) -> CredentialRequirement {
        if self.uses_vault_token {
            CredentialRequirement::VaultGitToken
        } else {
            CredentialRequirement::ExternalSshAgent
        }
    }
}

impl SyncAdapter for GitAdapter {
    fn upload_plan(&self, bundle: &EncryptedSyncBundle) -> Result<SyncTransferPlan> {
        Ok(SyncTransferPlan::Git {
            operation: SyncOperation::Upload,
            provider: self.provider,
            remote_url: self.remote_url.clone(),
            branch: self.branch.clone(),
            artifact_path: GIT_ARTIFACT_PATH.to_owned(),
            encrypted_bundle_sha256: bundle.encrypted_sha256()?,
            credential_requirement: self.credential_requirement(),
        })
    }

    fn download_plan(&self, bundle_fingerprint: String) -> Result<SyncTransferPlan> {
        Ok(SyncTransferPlan::Git {
            operation: SyncOperation::Download,
            provider: self.provider,
            remote_url: self.remote_url.clone(),
            branch: self.branch.clone(),
            artifact_path: GIT_ARTIFACT_PATH.to_owned(),
            encrypted_bundle_sha256: bundle_fingerprint,
            credential_requirement: self.credential_requirement(),
        })
    }
}

pub fn adapter_for(configuration: &SyncConfiguration) -> Result<Box<dyn SyncAdapter>> {
    configuration.validate()?;
    match configuration.kind() {
        SyncKind::WebDav => Ok(Box::new(WebDavAdapter::from_configuration(configuration)?)),
        SyncKind::Git => Ok(Box::new(GitAdapter::from_configuration(configuration)?)),
    }
}

#[cfg(test)]
mod tests {
    use std::sync::Mutex;

    use super::*;
    use crate::{
        domain::{SyncConfiguration, SyncConfigurationInput},
        vault::VaultEnvelope,
    };

    fn webdav_configuration() -> SyncConfiguration {
        SyncConfiguration::from_input(SyncConfigurationInput::WebDav {
            endpoint: "https://dav.example.test/root".to_owned(),
            remote_path: "nekossh/vault.bundle".to_owned(),
            username: Some("alice".to_owned()),
            secret: "webdav-secret-must-not-leak".to_owned(),
        })
        .expect("valid WebDAV configuration")
    }

    #[derive(Default)]
    struct RecordingWebDavTransport {
        request: Mutex<Option<(String, String, String)>>,
        condition: Mutex<Option<WebDavWriteCondition>>,
        uploaded: Mutex<Vec<u8>>,
        downloaded: Mutex<Option<Vec<u8>>>,
        etag: Mutex<Option<String>>,
    }

    impl WebDavTransport for RecordingWebDavTransport {
        fn put(
            &self,
            request: &WebDavRequest,
            encrypted_bundle: &[u8],
            condition: &WebDavWriteCondition,
        ) -> Result<String> {
            *self.request.lock().expect("request lock") = Some((
                request.url.clone(),
                request.authorization.to_string(),
                request.content_type.clone(),
            ));
            *self.condition.lock().expect("condition lock") = Some(condition.clone());
            *self.uploaded.lock().expect("upload lock") = encrypted_bundle.to_vec();
            self.etag
                .lock()
                .expect("etag lock")
                .clone()
                .ok_or(VaultError::SyncVersionUnavailable)
        }

        fn get(&self, _request: &WebDavRequest) -> Result<Vec<u8>> {
            self.downloaded
                .lock()
                .expect("download lock")
                .clone()
                .ok_or(VaultError::SyncTransportFailed)
        }
    }

    #[test]
    fn webdav_upload_only_moves_encrypted_bytes_and_uses_create_condition() {
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);
        let expected = bundle.to_bytes().expect("bundle bytes");
        let executor = WebDavSyncExecutor::new(RecordingWebDavTransport {
            etag: Mutex::new(Some("\"v1\"".to_owned())),
            ..Default::default()
        });
        let result = executor
            .upload(&webdav_configuration(), &bundle, None)
            .expect("upload succeeds");
        assert_eq!(result.receipt.kind, SyncKind::WebDav);
        assert_eq!(result.remote_etag, "\"v1\"");
        assert_eq!(
            *executor.transport.uploaded.lock().expect("upload lock"),
            expected
        );
        assert_eq!(
            *executor.transport.condition.lock().expect("condition lock"),
            Some(WebDavWriteCondition::CreateOnly)
        );
        let request = executor
            .transport
            .request
            .lock()
            .expect("request lock")
            .clone()
            .expect("recorded request");
        assert!(request.0.ends_with("/root/nekossh/vault.bundle"));
        assert!(request.1.starts_with("Basic "));
        assert!(
            !serde_json::to_string(&result.receipt)
                .expect("receipt JSON")
                .contains("webdav-secret-must-not-leak")
        );
    }

    #[test]
    fn webdav_upload_rejects_unsafe_etag() {
        assert!(matches!(
            validate_webdav_etag("\"valid\"\r\nX-Injected: yes"),
            Err(VaultError::SyncVersionUnavailable)
        ));
    }

    #[test]
    fn webdav_download_rejects_oversized_response() {
        let executor = WebDavSyncExecutor::new(RecordingWebDavTransport {
            downloaded: Mutex::new(Some(vec![0; MAX_SYNC_BUNDLE_BYTES + 1])),
            ..Default::default()
        });
        assert!(matches!(
            executor.download(&webdav_configuration()),
            Err(VaultError::SyncBundleTooLarge)
        ));
    }

    #[derive(Default)]
    struct RecordingGitRepository {
        exists: bool,
        fetched: bool,
        uploaded: Vec<u8>,
        push_succeeds: bool,
        downloaded: Option<Vec<u8>>,
        operations: Vec<&'static str>,
    }

    impl GitRepository for RecordingGitRepository {
        fn fetch_branch_if_present(&mut self) -> Result<bool> {
            self.operations.push("fetch");
            self.fetched = true;
            Ok(self.exists)
        }

        fn commit_bundle(&mut self, encrypted_bundle: &[u8]) -> Result<()> {
            self.operations.push("commit");
            self.uploaded = encrypted_bundle.to_vec();
            Ok(())
        }

        fn push_new_commit(&mut self) -> Result<bool> {
            self.operations.push("push");
            Ok(self.push_succeeds)
        }

        fn read_bundle(&mut self) -> Result<Vec<u8>> {
            self.operations.push("read");
            self.downloaded
                .clone()
                .ok_or(VaultError::SyncRemoteBundleMissing)
        }
    }

    #[test]
    fn git_upload_fetches_then_nonforce_pushes_only_encrypted_bundle() {
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);
        let expected = bundle.to_bytes().expect("bundle bytes");
        let mut executor = GitSyncExecutor::new(RecordingGitRepository {
            exists: true,
            push_succeeds: true,
            ..Default::default()
        });
        let receipt = executor.upload(&bundle).expect("upload succeeds");
        assert_eq!(receipt.kind, SyncKind::Git);
        assert_eq!(receipt.operation, SyncOperation::Upload);
        assert_eq!(executor.repository.operations, ["fetch", "commit", "push"]);
        assert_eq!(executor.repository.uploaded, expected);
    }

    #[test]
    fn git_push_rejection_never_reports_success() {
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);
        let mut executor = GitSyncExecutor::new(RecordingGitRepository {
            push_succeeds: false,
            ..Default::default()
        });
        assert!(matches!(
            executor.upload(&bundle),
            Err(VaultError::SyncGitPushRejected)
        ));
    }

    #[test]
    fn git_download_requires_existing_branch_and_parses_envelope() {
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);
        let mut executor = GitSyncExecutor::new(RecordingGitRepository {
            exists: true,
            downloaded: Some(bundle.to_bytes().expect("bundle bytes")),
            ..Default::default()
        });
        assert_eq!(executor.download().expect("download"), bundle);
        assert_eq!(executor.repository.operations, ["fetch", "read"]);
    }

    // This uses `file://` only inside the test process to exercise libgit2's real fetch, tree,
    // commit, non-force push, and read code without an account or network. Production request
    // construction remains HTTPS-token-only and rejects file URLs before this type is reachable.
    fn git2_repository_for_file_remote(remote_url: String) -> Git2Repository {
        let workspace = GitWorkspace::new().expect("temporary workspace");
        let mut options = RepositoryInitOptions::new();
        options
            .bare(true)
            .no_reinit(true)
            .external_template(false)
            .initial_head("main");
        let repository = Repository::init_opts(&workspace.root, &options).expect("local repo");
        Git2Repository {
            repository,
            request: GitTransferRequest {
                remote_url,
                branch: "main".to_owned(),
                username: "oauth2",
                token: StoredSecret::new("test-token-not-used-for-file-remote".to_owned()),
                origin: HttpsOrigin::from_remote("https://example.test/nekossh.git")
                    .expect("test origin"),
            },
            _workspace: workspace,
            remote_parent: None,
            pending_commit: None,
            test_allow_unauthenticated_file_remote: true,
        }
    }

    #[test]
    fn libgit2_real_round_trip_writes_only_the_encrypted_artifact() {
        let remote_workspace = GitWorkspace::new().expect("remote workspace");
        let remote_path = remote_workspace.root.join("remote.git");
        let _remote = Repository::init_bare(&remote_path).expect("bare remote");
        let remote_url = Url::from_file_path(&remote_path)
            .expect("file URL")
            .to_string();
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);

        let mut uploader =
            GitSyncExecutor::new(git2_repository_for_file_remote(remote_url.clone()));
        let receipt = uploader.upload(&bundle).expect("real libgit2 upload");
        assert_eq!(receipt.kind, SyncKind::Git);
        assert_eq!(receipt.operation, SyncOperation::Upload);
        drop(uploader);

        let mut downloader = GitSyncExecutor::new(git2_repository_for_file_remote(remote_url));
        assert_eq!(
            downloader.download().expect("real libgit2 download"),
            bundle
        );
    }

    #[test]
    fn git_configuration_and_plans_do_not_serialize_token() {
        let configuration = SyncConfiguration::from_input(SyncConfigurationInput::Git {
            provider: GitProvider::Github,
            remote_url: "https://github.com/example/nekossh.git".to_owned(),
            branch: "main".to_owned(),
            access_token: Some("github-token-must-not-leak".to_owned()),
        })
        .expect("Git config");
        let bundle = EncryptedSyncBundle::from_envelope(&VaultEnvelope::test_fixture(), 1);
        let plan = GitAdapter::from_configuration(&configuration)
            .expect("adapter")
            .upload_plan(&bundle)
            .expect("plan");
        assert!(
            !serde_json::to_string(&plan)
                .expect("plan JSON")
                .contains("github-token-must-not-leak")
        );
        assert!(git_transfer_request(&configuration).is_ok());
    }

    #[test]
    fn git_sync_safely_rejects_ssh_or_missing_token_until_host_keys_are_integrated() {
        for (remote_url, access_token) in [
            ("git@github.com:example/nekossh.git", None),
            ("https://github.com/example/nekossh.git", None),
        ] {
            let configuration = SyncConfiguration::from_input(SyncConfigurationInput::Git {
                provider: GitProvider::Github,
                remote_url: remote_url.to_owned(),
                branch: "main".to_owned(),
                access_token: access_token.map(str::to_owned),
            })
            .expect("valid stored config");
            assert!(matches!(
                git_transfer_request(&configuration),
                Err(VaultError::GitAuthenticationUnsupported)
            ));
        }
    }

    #[test]
    fn https_credential_callback_is_bound_to_configured_origin() {
        let origin =
            HttpsOrigin::from_remote("https://github.com/example/nekossh.git").expect("origin");
        assert!(origin.matches_callback_url("https://github.com/example/nekossh.git"));
        assert!(!origin.matches_callback_url("https://evil.example/redirect"));
        assert!(!origin.matches_callback_url("http://github.com/example/nekossh.git"));
    }
}
