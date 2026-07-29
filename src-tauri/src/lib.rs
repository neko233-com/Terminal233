//! NekoSSH native Tauri v2 SSH IDE backend.
//!
//! The desktop path is deliberately independent from the browser workspace. It keeps SSH
//! connection material in memory and uses Rust `ssh2` directly, without an external SSH client.

pub mod domain;
pub mod ephemeral;
pub mod error;
mod ide_commands;
mod sftp_transfer;
mod ssh;
mod ssh_terminal;

use tauri::Manager;

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
            ide_commands::ide_sftp_mkdir,
            ide_commands::ide_sftp_rename,
            ide_commands::ide_sftp_delete,
            ide_commands::ide_terminal_start,
            ide_commands::ide_terminal_write,
            ide_commands::ide_terminal_resize,
            ide_commands::ide_terminal_close,
        ])
        .run(tauri::generate_context!())
        .expect("error while running NekoSSH desktop application");
}

#[cfg(test)]
mod configuration_tests {
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
    }
}
