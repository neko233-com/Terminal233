# NekoSSH 测试说明

## 可复现的本地验证

```powershell
npm ci
npm run build
npm test
cargo test --manifest-path src-tauri/Cargo.toml
npm audit
```

本 checkout 在 2026-07-24 的本地验证结果为：

| 命令 | 实测结果 |
| --- | --- |
| `npm test` | 4 个测试文件、21 个测试通过。 |
| `cargo test --manifest-path src-tauri/Cargo.toml` | `nekossh_lib` 38 个单元测试通过；`main` 与 doc-tests 当前均为 0。 |

这些是当前代码与锁定依赖的可核验计数，不是永久承诺；增加或删除测试时，应同时更新本表并以命令输出为准。

## 已覆盖的自动化边界

| 层级 | 当前覆盖 |
| --- | --- |
| Web 前端预览 | 工作区切换、只读终端/桌面预览、预览不采集 Vault/云凭据、不建立 SSH、浏览器 URL 预校验与用户显式新标签打开。 |
| 桌面前端桥接 | Vault/Profile 加载、受限 `profile_create`、外部 SSH 启动、WebDAV/Git 上传命令、Rust 内恢复命令和 Git HTTPS token 表单约束。 |
| 加密 Vault | 新建/锁定/解锁、改密重新包装、格式篡改、KDF 下限/上限、AEAD 关联数据、导入密码验证、锁定要求、`.bak` 回滚与恢复。 |
| SSH / opener | 无 shell 的 OpenSSH 参数、应用专属 known-hosts、`accept-new`/禁用全局 known-hosts、Windows Terminal argv、固定 Windows 程序路径约束、CSP/capability 与禁止自动 opener 链接。 |
| 同步 | WebDAV 加密字节、创建条件、ETag 校验、下载大小限制；Git token 不泄露、HTTPS origin 绑定、非强制 push 拒绝、Git SSH/token 缺失拒绝，以及真实 libgit2 本地 `file://` round-trip。 |

WebDAV 自动测试使用受控 transport double，不是实际 WebDAV 服务器；Git 的“真实”回环测试使用本地 `file://` bare remote，不测试互联网 HTTPS、令牌提供商或证书链。测试通过不等于这些外部服务已验收。

## 发布前手工验收

1. 在干净桌面环境创建 Vault；短于 12 字符的主密码必须失败，改密后旧密码必须无法解锁。
2. 尝试导入错误密码、篡改 nonce/密文/KDF/格式的包；本地 Vault 不得被锁定、替换或损坏。
3. 使用真实 HTTPS WebDAV：首次上传、带 ETag 的后续上传、409/412 冲突、缺失 ETag、重定向、无效证书、超时和大于 16 MiB 下载都应安全失败。
4. 使用真实 GitHub/Gitea HTTPS 私有仓库与最小权限 token：确认 token 不出现在 remote URL、进程、日志或回执；模拟并发 push，确认不会 force-push 覆盖远端。
5. 对 WebDAV 和 Git 分别执行安全恢复：下载后先以备份密码验证，导入后 Vault 保持锁定；再用导入前旧密码验证 `.bak` 回滚。
6. Windows 上确认仅在 `%SystemRoot%\System32\OpenSSH\ssh.exe` 和 `%LOCALAPPDATA%\Microsoft\WindowsApps\wt.exe` 存在时启动；新标签中的第一次连接写入应用专属 `neko233-known_hosts`，变更主机密钥必须被拒绝。
7. 确认浏览器、桌面和终端预览不显示“已连接”、不执行命令、不读取远端内容，也不向 Web 运行时暴露 Vault 凭据。

## 仍需补齐的测试

- 真实 WebDAV 服务和真实 HTTPS Git 服务的隔离集成测试（含 TLS、重定向、认证失败、冲突与网络重试）。
- Tauri 桌面端到端测试：capability、CSP、外部 opener、Windows Terminal、应用数据目录权限和恢复 UI。
- 真实 SSHD + PTY 集成测试；Vault 内 PEM 私钥的内嵌 transport 目前尚未实现。
- 多设备同步的版本选择/合并 UX、可访问性、窄屏布局和视觉回归测试。

在这些外部集成与发布测试接入 CI 前，NekoSSH 应保持为明确标记边界的 0.1 原型。
