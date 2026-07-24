//! NekoSSH desktop backend.
//!
//! The desktop shell receives only the narrow IPC commands registered in `run`.  Sensitive vault
//! values are encrypted at rest and are never returned to frontend code after profile creation.

mod commands;
pub mod crypto;
pub mod domain;
pub mod error;
pub mod sync;
pub mod vault;

use tauri::Manager;

pub use commands::VaultState;

pub fn run() {
    tauri::Builder::default()
        // Do not let the opener plugin inject its default `_blank`/modified-click handler into
        // the webview.  Browser launches must pass through `browser_open_url`, which validates
        // scheme, host, and embedded credentials in Rust first.
        .plugin(
            tauri_plugin_opener::Builder::new()
                .open_js_links_on_click(false)
                .build(),
        )
        .setup(|app| {
            let vault_path = app.path().app_data_dir()?.join(vault::VAULT_FILENAME);
            app.manage(commands::VaultState::new(vault_path));
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            commands::vault_status,
            commands::vault_bootstrap,
            commands::vault_unlock,
            commands::vault_lock,
            commands::vault_change_password,
            commands::profile_create,
            commands::profile_list,
            commands::profile_get,
            commands::ssh_session_plan,
            commands::ssh_launch_external,
            commands::profile_update,
            commands::profile_delete,
            commands::sync_configure,
            commands::sync_configuration,
            commands::sync_export_encrypted_bundle,
            commands::sync_verify_encrypted_bundle,
            commands::sync_import_encrypted_bundle,
            commands::sync_restore_import_backup,
            commands::sync_upload_plan,
            commands::sync_webdav_upload,
            commands::sync_webdav_download_encrypted_bundle,
            commands::sync_git_upload,
            commands::sync_git_download_encrypted_bundle,
            commands::sync_git_restore_import,
            commands::sync_webdav_restore_import,
            commands::browser_open_url,
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
    fn capability_does_not_expose_the_opener_plugin_to_webview_code() {
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
    }

    #[test]
    fn opener_plugin_does_not_install_automatic_webview_link_handlers() {
        assert!(include_str!("lib.rs").contains("open_js_links_on_click(false)"));
    }
}
