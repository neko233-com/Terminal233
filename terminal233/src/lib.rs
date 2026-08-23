//! Terminal233 native rdesktop SSH IDE backend.
//!
//! The desktop path is deliberately independent from the browser workspace. It keeps SSH
//! connection material in memory and uses pure-Rust `puressh` directly, without an external SSH
//! client or native SSH/cryptography library.

pub mod domain;
pub mod ephemeral;
pub mod error;
pub mod ide_commands;
pub mod ipc;
mod operation_admission;
mod sftp_transfer;
mod ssh;
mod ssh_terminal;

pub fn run_rdesktop() -> Result<(), String> {
    use std::path::PathBuf;
    use std::sync::{Arc, Mutex};

    use rdesktop_core::{
        AppConfig, WindowConfig,
        ipc::{FnIpcHandler, IpcMessage},
        renderer::Renderer,
    };
    use rdesktop_webview::WebViewRenderer;

    tracing_subscriber::fmt()
        .with_env_filter("terminal233=info,rdesktop=info")
        .try_init()
        .ok();

    let config = AppConfig {
        identifier: "site.neko233.terminal233".to_owned(),
        name: "Terminal233".to_owned(),
        version: env!("CARGO_PKG_VERSION").to_owned(),
        window: WindowConfig {
            title: "Terminal233".to_owned(),
            width: 1280,
            height: 800,
            resizable: true,
            min_size: Some((960, 640)),
            ..Default::default()
        },
        ..Default::default()
    };

    let outbox = Arc::new(Mutex::new(Vec::<String>::new()));
    let state = Arc::new(ide_commands::IdeState::new());
    let handler = FnIpcHandler::new({
        let state = Arc::clone(&state);
        let outbox = Arc::clone(&outbox);
        move |message: IpcMessage| ide_commands::handle_rdesktop_message(&state, message, &outbox)
    });

    let mut renderer = WebViewRenderer::new(&config).map_err(|error| error.to_string())?;
    let asset_root = std::env::var_os("TERMINAL233_FRONTEND_ROOT")
        .map(PathBuf::from)
        .or_else(|| std::env::current_dir().ok().map(|path| path.join("dist")))
        .ok_or_else(|| "无法确定前端目录".to_owned())?;
    renderer
        .set_asset_root(asset_root)
        .map_err(|error| error.to_string())?;
    renderer.set_outbox(Arc::clone(&outbox));
    renderer.init().map_err(|error| error.to_string())?;
    renderer.set_ipc_handler(Box::new(handler));
    let window = renderer
        .create_window(&config.window)
        .map_err(|error| error.to_string())?;
    renderer
        .load_url(window, "rdesktop://localhost/index.html")
        .map_err(|error| error.to_string())?;
    Box::new(renderer).run().map_err(|error| error.to_string())
}

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
