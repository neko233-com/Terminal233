//! NekoSSH native Tauri v2 SSH IDE backend.
//!
//! The desktop path is deliberately independent from the browser workspace. It keeps SSH
//! connection material in memory and uses pure-Rust `puressh` directly, without an external SSH
//! client or native SSH/cryptography library.

pub mod domain;
pub mod ephemeral;
pub mod error;
mod ide_commands;
mod operation_admission;
mod sftp_transfer;
mod ssh;
mod ssh_terminal;

use tauri::Manager;

fn initialize_puressh_runtime() -> Result<(), String> {
    use std::sync::{Arc, Mutex};

    use puressh::{
        client::{AlgoOverrides, Config},
        known_hosts::KnownHosts,
    };

    const KEX_ALGORITHMS: &[&str] = &["curve25519-sha256"];
    const HOST_KEY_ALGORITHMS: &[&str] = &[
        "ssh-ed25519",
        "ecdsa-sha2-nistp256",
        "rsa-sha2-512",
        "rsa-sha2-256",
    ];
    const CIPHERS: &[&str] = &[
        "chacha20-poly1305@openssh.com",
        "aes256-gcm@openssh.com",
        "aes128-gcm@openssh.com",
    ];
    const MACS: &[&str] = &[
        "hmac-sha2-256-etm@openssh.com",
        "hmac-sha2-512-etm@openssh.com",
    ];

    if KEX_ALGORITHMS.first() != Some(&puressh::kex::Curve25519Sha256::NAME) {
        return Err("纯 Rust SSH 运行时缺少 Curve25519 密钥交换".to_owned());
    }
    if HOST_KEY_ALGORITHMS
        .iter()
        .any(|name| !puressh::hostkey::HOST_KEY_VERIFY_NAMES.contains(name))
    {
        return Err("纯 Rust SSH 运行时缺少现代主机密钥算法".to_owned());
    }
    if CIPHERS
        .iter()
        .any(|name| puressh::cipher::by_name(name).is_none())
    {
        return Err("纯 Rust SSH 运行时缺少现代加密算法".to_owned());
    }
    if MACS
        .iter()
        .any(|name| puressh::mac::by_name(name).is_none())
    {
        return Err("纯 Rust SSH 运行时缺少 Encrypt-then-MAC 算法".to_owned());
    }
    if puressh::hostkey::allow_rsa_sha1() {
        return Err("纯 Rust SSH 运行时意外启用了旧版 ssh-rsa/SHA-1".to_owned());
    }

    let mut config = Config::with_known_hosts(Arc::new(Mutex::new(KnownHosts::new())));
    config.algorithms = AlgoOverrides {
        ciphers: Some(CIPHERS.iter().map(|name| (*name).to_owned()).collect()),
        macs: Some(MACS.iter().map(|name| (*name).to_owned()).collect()),
        kex_algorithms: Some(
            KEX_ALGORITHMS
                .iter()
                .map(|name| (*name).to_owned())
                .collect(),
        ),
        host_key_algorithms: Some(
            HOST_KEY_ALGORITHMS
                .iter()
                .map(|name| (*name).to_owned())
                .collect(),
        ),
        pubkey_accepted_algorithms: Some(
            HOST_KEY_ALGORITHMS
                .iter()
                .map(|name| (*name).to_owned())
                .collect(),
        ),
        ca_signature_algorithms: None,
        compression: Some(false),
    };
    let _configured_runtime = config;
    Ok(())
}

pub fn production_self_test_receipt() -> Result<String, String> {
    initialize_puressh_runtime()?;
    let state = ide_commands::IdeState::new();
    drop(state);
    Ok(serde_json::json!({
        "ok": true,
        "version": env!("CARGO_PKG_VERSION"),
        "runtime": "pure-rust-puressh",
        "persistence": "memory_only",
    })
    .to_string())
}

pub fn run() {
    tauri::Builder::default()
        .setup(|app| {
            app.manage(ide_commands::IdeState::new());
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            ide_commands::ide_connection_create,
            ide_commands::ide_connection_list,
            ide_commands::ide_connection_disconnect,
            ide_commands::ide_ssh_prepare,
            ide_commands::ide_ssh_trust_host_key,
            ide_commands::ide_ssh_reject_host_key,
            ide_commands::ide_ssh_exec,
            ide_commands::ide_sftp_list,
            ide_commands::ide_sftp_upload_start,
            ide_commands::ide_sftp_download_start,
            ide_commands::ide_sftp_transfer_cancel,
            ide_commands::ide_sftp_download_ack,
            ide_commands::ide_sftp_mkdir,
            ide_commands::ide_sftp_rename,
            ide_commands::ide_sftp_delete,
            ide_commands::ide_terminal_start,
            ide_commands::ide_terminal_write,
            ide_commands::ide_terminal_resize,
            ide_commands::ide_terminal_ack_output,
            ide_commands::ide_terminal_close,
        ])
        .run(tauri::generate_context!())
        .expect("error while running NekoSSH desktop application");
}

#[cfg(test)]
mod configuration_tests {
    #[test]
    fn production_self_test_initializes_the_native_runtime() {
        let receipt: serde_json::Value =
            serde_json::from_str(&super::production_self_test_receipt().expect("self-test"))
                .expect("JSON receipt");
        assert_eq!(receipt["ok"].as_bool(), Some(true));
        assert_eq!(receipt["runtime"].as_str(), Some("pure-rust-puressh"));
        assert_eq!(receipt["persistence"].as_str(), Some("memory_only"));
    }

    #[test]
    fn tauri_configuration_uses_loopback_dev_server_and_a_restrictive_csp() {
        let config: serde_json::Value =
            serde_json::from_str(include_str!("../tauri.conf.json")).expect("valid Tauri JSON");
        assert_eq!(
            config["build"]["devUrl"].as_str(),
            Some("http://127.0.0.1:1420")
        );
        let csp = config["app"]["security"]["csp"]
            .as_str()
            .expect("CSP must be configured");
        assert!(csp.contains("default-src 'self'"));
        assert!(csp.contains("connect-src 'self' ipc: http://ipc.localhost"));
        assert!(csp.contains("object-src 'none'"));
        assert!(csp.contains("frame-src 'none'"));
        assert!(!csp.contains("unsafe-eval"));
        assert!(!csp.contains("127.0.0.1:1420"));
        assert!(!csp.contains("ws://"));
        let dev_csp = config["app"]["security"]["devCsp"]
            .as_str()
            .expect("development CSP must be configured separately");
        assert!(dev_csp.contains("http://127.0.0.1:1420"));
        assert!(dev_csp.contains("ws://127.0.0.1:1420"));
        assert_eq!(
            config["app"]["security"]["capabilities"],
            serde_json::json!(["main-capability"])
        );
        assert_eq!(
            config["app"]["windows"][0]["incognito"].as_bool(),
            Some(true)
        );
        // Tauri's native file-drop handler intercepts HTML5 drag events on Windows. The IDE
        // deliberately uses WebView drag/drop for connection ordering and SFTP uploads.
        assert_eq!(
            config["app"]["windows"][0]["dragDropEnabled"].as_bool(),
            Some(false)
        );
        assert_eq!(
            config["bundle"]["windows"]["allowDowngrades"].as_bool(),
            Some(false)
        );
        assert_eq!(config["bundle"]["licenseFile"].as_str(), Some("../LICENSE"));
        assert_eq!(
            config["bundle"]["windows"]["wix"]["upgradeCode"].as_str(),
            Some("44f1588e-bcfd-5973-a955-237daf92726d")
        );
    }

    #[test]
    fn capability_does_not_expose_external_process_or_opener_permissions() {
        let capability: serde_json::Value =
            serde_json::from_str(include_str!("../capabilities/default.json"))
                .expect("valid capability JSON");
        let permissions = capability["permissions"]
            .as_array()
            .expect("capability permissions");
        assert!(permissions.iter().all(|permission| {
            !permission
                .as_str()
                .is_some_and(|permission| permission.starts_with("opener:"))
        }));
        assert!(permissions.iter().all(|permission| {
            !permission
                .as_str()
                .is_some_and(|permission| permission.starts_with("shell:"))
        }));
        assert_eq!(permissions.len(), 20);
        assert!(permissions.iter().all(|permission| {
            permission
                .as_str()
                .is_some_and(|permission| permission.starts_with("allow-ide-"))
        }));
        assert!(
            !permissions
                .iter()
                .any(|permission| permission.as_str() == Some("core:default"))
        );
    }
}
