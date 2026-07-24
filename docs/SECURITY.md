# NekoSSH 安全说明

本文描述当前实现已落地的安全属性与仍然存在的产品边界。它不是独立安全审计或生产认证；在真实服务端和目标平台完成端到端验证前，不应把 0.1 当作最终凭据管理器。

## 资产与信任边界

需要保护的资产包括主密码、SSH 密码和私钥、WebDAV 凭据、Git token、主机资料、云端密文包及其版本状态。远端存储、网络、不可信网页和用户输入都视为不可信。

| 边界 | 已实现保护 | 不应假设 |
| --- | --- | --- |
| Vault | 资料由随机 DEK 加密、认证；导入前会验证格式、KDF、密码包装和 AEAD 负载。 | 密文备份不等于无需密码；弱主密码仍可能遭受离线猜测。 |
| Tauri IPC | 命令返回摘要、回执或密文包，不返回 SSH 密码、私钥、WebDAV secret 或 Git token。主密码在命令边界用 `Zeroizing` 保存。 | 前端状态不能替代后端回执，也不应被视为已连接。 |
| 外部 SSH | 参数以 argv 传递，不经过 shell；主机密钥使用 NekoSSH 专属 OpenSSH 文件。 | 没有内嵌 PTY，Vault 内 PEM 私钥也不会被临时写入磁盘。 |
| WebDAV | Rustls HTTPS、30 秒全局超时、禁重定向、16 MiB 下载上限、ETag 条件写入。 | 不能自动合并两个设备的冲突；冲突必须由用户下载、审阅和恢复。 |
| Git | 进程内 libgit2、HTTPS token 回调仅匹配配置 origin、禁重定向、非强制 push。 | Git SSH / SSH Agent 明确拒绝；不会为了兼容性放宽主机密钥校验。 |
| 浏览器 | WebView 无 opener 权限；自动链接打开已关闭，Rust 只接受 HTTPS 或无凭据的 loopback HTTP。 | 不可信网站不会嵌入拥有 Tauri IPC 的 WebView，也不应获取 Vault 数据。 |

## 保险箱与 KDF

`neko233-v1` 是公开格式标识，并作为 AEAD 关联数据的一部分；它**不是**密码、密钥或种子。创建 Vault 时：

1. 生成随机 256-bit 数据加密密钥（DEK）。
2. 为主密码生成随机 128-bit salt，使用 Argon2id v1.3 派生 256-bit KEK。
3. 用 KEK 包装 DEK，再以 DEK 用 XChaCha20-Poly1305 加密并认证资料负载。
4. 仅持久化格式、KDF 元数据、nonce 和密文/认证标签。

默认 Argon2id 参数为 64 MiB、3 次迭代、并行度 1。外来/已落盘的 KDF 元数据在分配内存前必须满足：内存 32–128 MiB、迭代最多 6、并行度最多 4。此上限可避免恶意或损坏的云备份在密码提示期间申请多 GiB 内存。

主密码最少 12 个字符。改密会重新包装 DEK，不重写资料密文；旧密码不能再解锁。没有找回通道：若只剩密文而主密码遗失，资料无法恢复。

## 传统 SSH 与主机密钥

传统 SSH 以系统 OpenSSH 运行，不构造 shell 命令。应用为每个 Vault 使用同目录的专属 `neko233-known_hosts` 文件，并给 OpenSSH 传入：

- `StrictHostKeyChecking=accept-new`：第一次看到的主机密钥写入应用文件；以后变化的密钥被拒绝。
- `UserKnownHostsFile=<应用专属绝对路径>`：不与个人全局记录混用。
- `GlobalKnownHostsFile=none` 与 `UpdateHostKeys=no`：不静默接受机器范围的记录或额外的服务器键更新。

这仍是 trust-on-first-use（TOFU）。初次连接敏感主机时，应通过可信渠道比对服务器 SHA-256 指纹；不要把“首次自动记录”误解为已验证身份。

在 Windows，NekoSSH 不从 `PATH` 查找可执行文件：只接受存在的 `%SystemRoot%\System32\OpenSSH\ssh.exe` 和 `%LOCALAPPDATA%\Microsoft\WindowsApps\wt.exe`，并通过 Windows Terminal 以单独 argv 启动新标签。这样用户名、主机名和密钥文件路径不会进入 `cmd.exe`、PowerShell 或字符串命令解析器。没有这些受信任路径时，启动会失败而不是退回到可写 PATH 目录。

Vault 内直接保存的 PEM 私钥不会落盘，因此外部 OpenSSH 不支持它；须使用经审计的内嵌 SSH/PTY 适配器后才可支持。私钥文件、Agent 和外部密码提示资料可生成受校验的外部 OpenSSH 调用，但 GUI 内仍没有终端 PTY。

## 加密云同步

WebDAV secret 仅在本地内存中构造 HTTP `Authorization` 头，且因禁重定向不会被重放到未配置 URL。首次写入使用 `If-None-Match: *`；成功响应必须提供安全 ETag。该 ETag 与 WebDAV 端点和远端路径一起加密保存在 Vault 中，后续写入使用 `If-Match`。服务器的 409/412 或本地配置在网络期间变化都会返回冲突而不认可/覆盖状态。

Git 执行器只接受 HTTPS remote 和 Vault 内 token。libgit2 的凭据回调只向配置的 scheme、host 和 port 提供 token；token 不会出现在 URL、环境变量、命令行、Git 配置、计划、回执或日志。上传先获取当前分支，再创建只含 `.nekossh/vault.bundle` 的提交，并进行非强制 push；远端拒绝或并发变化会失败，绝不强制覆盖。

Git 的 `ssh://` 和 `git@host:path` 配置会在执行时明确报为不受支持，直至 Git SSH 可以使用与 NekoSSH 相同的主机密钥验证策略。不要将此错误绕过为未审计的 SSH Agent 连接。

## 安全恢复与回滚

WebDAV/Git 的安全恢复命令在 Rust 内执行，不让下载的密文包往返 WebView：

1. 从当前已解锁 Vault 读取配置和其加密封套快照，再下载远端密文包。
2. 在触碰本地 Vault 前，用用户提供的备份主密码验证 KDF、包装 DEK、AEAD 负载与语义数据。
3. 确认运行时的 Vault/同步配置在下载期间未改变；若改变则以冲突失败。
4. 清除内存中的解锁状态，将当前有效 Vault 原子写入相邻 `neko233-v1.vault.json.bak`。
5. 原子替换为已验证的远端封套，并保持锁定。

导入失败或密码错误不会替换当前 Vault。回滚命令要求 Vault 保持锁定，并要求输入**导入前本地 Vault 的旧主密码**来验证 `.bak`；回滚副本不会在失败恢复时被销毁。

## 浏览器与 opener 权限

应用 capability 只授予 `core:default`，没有把 opener 插件暴露给 WebView；插件自动处理网页链接已关闭。只有 Rust 的 `browser_open_url` 命令能调用系统浏览器，并会拒绝自定义协议、凭据嵌入 URL、非 loopback HTTP 与无 host 的地址。浏览器/桌面预览不会嵌入远端内容、执行 SSH 或收集 Vault 与云凭据。

## 发布前仍需验证

- 用真实 HTTPS WebDAV 服务测试 ETag、409/412、证书失败、重定向、断网和恢复/回滚。
- 用真实 GitHub/Gitea HTTPS token 测试 libgit2 认证、非强制 push 冲突和下载恢复；当前自动测试的真实 libgit2 回环使用本地 `file://` remote，不代替外网测试。
- 完成内嵌 SSH/PTY、Vault 内 PEM 私钥和高风险主机的交互式指纹确认后，再宣称完整 SSH 工作台。
- 继续审查 CSP、Tauri capability、签名、更新链、依赖/SBOM 与跨平台 Windows Terminal/OpenSSH 行为。
