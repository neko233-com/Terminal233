fn main() {
    const COMMANDS: &[&str] = &[
        "ide_connection_create",
        "ide_connection_list",
        "ide_connection_disconnect",
        "ide_ssh_prepare",
        "ide_ssh_trust_host_key",
        "ide_ssh_reject_host_key",
        "ide_ssh_exec",
        "ide_sftp_list",
        "ide_sftp_upload_start",
        "ide_sftp_download_start",
        "ide_sftp_transfer_cancel",
        "ide_sftp_download_ack",
        "ide_sftp_mkdir",
        "ide_sftp_rename",
        "ide_sftp_delete",
        "ide_terminal_start",
        "ide_terminal_write",
        "ide_terminal_resize",
        "ide_terminal_ack_output",
        "ide_terminal_close",
    ];

    tauri_build::try_build(
        tauri_build::Attributes::new()
            .app_manifest(tauri_build::AppManifest::new().commands(COMMANDS)),
    )
    .expect("failed to build the NekoSSH Tauri application");
}
