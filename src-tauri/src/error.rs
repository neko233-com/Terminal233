use thiserror::Error;

/// Native SSH errors never format credentials, key material, remote output, or library details
/// across IPC. `public_message` is the only renderer-facing description.
#[derive(Debug, Error)]
pub enum NekoError {
    #[error("the SSH service is closing")]
    Locked,
    #[error("invalid SSH profile: {0}")]
    InvalidProfile(&'static str),
    #[error("profile was not found")]
    ProfileNotFound,
    #[error("too many ephemeral SSH profiles are active")]
    ProfileLimit,
    #[error("unable to establish the SSH connection")]
    SshConnectionFailed,
    #[error("unable to complete the SSH handshake")]
    SshHandshakeFailed,
    #[error("the SSH server did not present a supported host key")]
    SshHostKeyUnavailable,
    #[error("the SSH server presented a rejected host-key algorithm")]
    SshHostKeyAlgorithmRejected,
    #[error("the SSH host key is not trusted yet")]
    SshHostKeyUntrusted,
    #[error("the SSH host key does not match the trusted key")]
    SshHostKeyMismatch,
    #[error("the SSH host-key confirmation has expired")]
    SshHostKeyConfirmationExpired,
    #[error("too many SSH host-key confirmations are pending")]
    SshHostKeyConfirmationLimit,
    #[error("SSH authentication failed")]
    SshAuthenticationFailed,
    #[error("the SSH command is invalid")]
    SshCommandInvalid,
    #[error("the SSH command could not complete")]
    SshCommandFailed,
    #[error("the remote directory path is invalid")]
    SshDirectoryPathInvalid,
    #[error("the remote directory could not be listed")]
    SshDirectoryListFailed,
    #[error("the SFTP transfer request is invalid")]
    SshTransferInputInvalid,
    #[error("too many SFTP transfer jobs are active")]
    SshTransferJobLimit,
    #[error("an SFTP transfer file exceeds the supported size")]
    SshTransferFileTooLarge,
    #[error("the combined SFTP transfer exceeds the supported size")]
    SshTransferTotalTooLarge,
    #[error("the SFTP transfer job was not found")]
    SshTransferJobNotFound,
    #[error("the SSH server does not support atomic SFTP overwrite")]
    SshAtomicOverwriteUnsupported,
    #[error("the SFTP transfer could not complete")]
    SshTransferFailed,
    #[error("the SSH terminal could not be started")]
    SshTerminalStartFailed,
    #[error("the SSH terminal size is invalid")]
    SshTerminalResizeInvalid,
    #[error("too many SSH terminal sessions are open")]
    SshTerminalSessionLimit,
    #[error("the SSH terminal session was not found")]
    SshTerminalSessionNotFound,
    #[error("the SSH terminal input is too large")]
    SshTerminalInputTooLarge,
    #[error("the SSH terminal input queue is busy")]
    SshTerminalInputBackpressure,
    #[error("an internal state lock was poisoned")]
    LockPoisoned,
}

impl NekoError {
    /// Stable IPC code with no implementation detail.
    pub const fn code(&self) -> &'static str {
        match self {
            Self::Locked => "ssh_service_closing",
            Self::InvalidProfile(_) => "invalid_profile",
            Self::ProfileNotFound => "profile_not_found",
            Self::ProfileLimit => "profile_limit",
            Self::SshConnectionFailed => "ssh_connection_failed",
            Self::SshHandshakeFailed => "ssh_handshake_failed",
            Self::SshHostKeyUnavailable => "ssh_host_key_unavailable",
            Self::SshHostKeyAlgorithmRejected => "ssh_host_key_algorithm_rejected",
            Self::SshHostKeyUntrusted => "ssh_host_key_untrusted",
            Self::SshHostKeyMismatch => "ssh_host_key_mismatch",
            Self::SshHostKeyConfirmationExpired => "ssh_host_key_confirmation_expired",
            Self::SshHostKeyConfirmationLimit => "ssh_host_key_confirmation_limit",
            Self::SshAuthenticationFailed => "ssh_authentication_failed",
            Self::SshCommandInvalid => "ssh_command_invalid",
            Self::SshCommandFailed => "ssh_command_failed",
            Self::SshDirectoryPathInvalid => "ssh_directory_path_invalid",
            Self::SshDirectoryListFailed => "ssh_directory_list_failed",
            Self::SshTransferInputInvalid => "ssh_transfer_input_invalid",
            Self::SshTransferJobLimit => "ssh_transfer_job_limit",
            Self::SshTransferFileTooLarge => "ssh_transfer_file_too_large",
            Self::SshTransferTotalTooLarge => "ssh_transfer_total_too_large",
            Self::SshTransferJobNotFound => "ssh_transfer_job_not_found",
            Self::SshAtomicOverwriteUnsupported => "ssh_sftp_atomic_overwrite_unsupported",
            Self::SshTransferFailed => "ssh_transfer_failed",
            Self::SshTerminalStartFailed => "ssh_terminal_start_failed",
            Self::SshTerminalResizeInvalid => "ssh_terminal_resize_invalid",
            Self::SshTerminalSessionLimit => "ssh_terminal_session_limit",
            Self::SshTerminalSessionNotFound => "ssh_terminal_session_not_found",
            Self::SshTerminalInputTooLarge => "ssh_terminal_input_too_large",
            Self::SshTerminalInputBackpressure => "ssh_terminal_input_backpressure",
            Self::LockPoisoned => "internal_lock_failed",
        }
    }

    /// Stable, non-sensitive Chinese renderer message.
    pub fn public_message(&self) -> String {
        match self {
            Self::Locked => "连接正在关闭，请稍后重试。",
            Self::InvalidProfile(_) => "连接配置无效，请检查主机、端口和认证方式。",
            Self::ProfileNotFound => "未找到该临时连接，可能已断开或过期。",
            Self::ProfileLimit => "临时连接数量已达到上限，请先断开不再使用的连接。",
            Self::SshConnectionFailed => "无法连接到 SSH 服务器，请检查主机、端口和网络。",
            Self::SshHandshakeFailed => "SSH 握手失败，请检查服务器兼容性。",
            Self::SshHostKeyUnavailable => "服务器未提供受支持的主机密钥。",
            Self::SshHostKeyAlgorithmRejected => "服务器使用了已禁用的旧主机密钥算法。",
            Self::SshHostKeyUntrusted => "该服务器主机密钥尚未确认。",
            Self::SshHostKeyMismatch => "服务器主机密钥与已确认的密钥不一致，连接已阻止。",
            Self::SshHostKeyConfirmationExpired => "主机密钥确认已过期，请重新连接。",
            Self::SshHostKeyConfirmationLimit => "待确认的主机密钥过多，请先关闭其他连接窗口。",
            Self::SshAuthenticationFailed => "SSH 认证失败，请检查用户名和凭据。",
            Self::SshCommandInvalid => "远程命令无效。",
            Self::SshCommandFailed => "远程命令执行失败。",
            Self::SshDirectoryPathInvalid => "远程目录路径无效。",
            Self::SshDirectoryListFailed => "无法读取远程目录。",
            Self::SshTransferInputInvalid => "SFTP 传输参数无效。",
            Self::SshTransferJobLimit => "正在进行的 SFTP 传输过多，请稍后重试。",
            Self::SshTransferFileTooLarge => "传输文件超过支持的大小。",
            Self::SshTransferTotalTooLarge => "本次传输总大小超过限制。",
            Self::SshTransferJobNotFound => "未找到该 SFTP 传输任务。",
            Self::SshAtomicOverwriteUnsupported => {
                "服务器不支持安全原子覆盖（posix-rename@openssh.com），原文件已保留。"
            }
            Self::SshTransferFailed => "SFTP 传输失败，请检查连接和远端权限。",
            Self::SshTerminalStartFailed => "无法启动 SSH 终端。",
            Self::SshTerminalResizeInvalid => "终端尺寸无效。",
            Self::SshTerminalSessionLimit => "打开的 SSH 终端过多，请先关闭其他终端。",
            Self::SshTerminalSessionNotFound => "SSH 终端会话不存在或已关闭。",
            Self::SshTerminalInputTooLarge => "终端输入超过允许的大小。",
            Self::SshTerminalInputBackpressure => "终端暂时繁忙，请稍后继续输入。",
            Self::LockPoisoned => "内部状态暂时不可用，请重试。",
        }
        .to_owned()
    }
}

pub type Result<T> = std::result::Result<T, NekoError>;

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn public_messages_hide_internal_validation_details() {
        let error = NekoError::InvalidProfile("password=do-not-display");
        assert_eq!(
            error.public_message(),
            "连接配置无效，请检查主机、端口和认证方式。"
        );
        assert!(!error.public_message().contains("do-not-display"));
        assert_eq!(
            NekoError::SshHostKeyAlgorithmRejected.public_message(),
            "服务器使用了已禁用的旧主机密钥算法。"
        );
        assert_eq!(
            NekoError::SshAtomicOverwriteUnsupported.public_message(),
            "服务器不支持安全原子覆盖（posix-rename@openssh.com），原文件已保留。"
        );
        assert_eq!(
            NekoError::SshAtomicOverwriteUnsupported.code(),
            "ssh_sftp_atomic_overwrite_unsupported"
        );
    }
}
