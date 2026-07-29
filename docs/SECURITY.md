# NekoSSH 安全说明

## 信任边界

| 边界 | 实现 |
| --- | --- |
| Web macOS | 外部浏览器只访问 `127.0.0.1`；SSH/SFTP 由本地 Node.js `ssh2` 进程执行 |
| Tauri v2 | Renderer 只调用窄 IPC；Rust `ssh2` 直接建立 TCP、SSH、PTY 与 SFTP |
| 运行时隔离 | 两端不共享凭据、主机信任、SSH transport、终端会话或 SFTP 任务 |
| 系统客户端 | 两条链路都不调用 `ssh.exe`、PuTTY、plink、Windows Terminal 或 shell |
| 持久化 | 连接、密码、主机信任、终端与 Agent grant 仅保存在所属进程内 |

## 主机密钥

Web 和 Tauri 都先完成 SSH 握手并返回服务器算法与 SHA-256 指纹。未知密钥必须由用户确认；确认 token 与具体 connection ID 绑定且短时有效。确认前不会继续认证。连接期间出现不同指纹会被拒绝，过时的 `ssh-dss`/DSA 主机密钥不会进入信任流程。

Web 探测连接只读取指纹，不携带密码并立即关闭；用户确认后新建认证连接，并再次用保存的指纹验证服务器。Tauri 使用 Rust `ssh2` 的 prepare/trust 流程，并在每次新连接时重新验证。

## Web localhost 服务

- 固定绑定 `127.0.0.1`，不监听局域网地址。
- 浏览器 API 校验允许的 loopback Origin，不开放 CORS。
- 浏览器会话使用随机 HttpOnly、SameSite=Strict cookie，30 分钟空闲过期。
- 每个页面拥有独立释放令牌；刷新、退出、取消、失败或指纹不匹配都会级联关闭 owner 的终端、授权与 SSH 连接。
- 密码不进入 URL、日志或 MCP transcript；断开时覆盖持有密码的 Buffer 并关闭连接。
- 连接、确认、授权、attachment 与终端表均有硬数量上限；普通 JSON 请求限制 64 KiB。
- SFTP、exec 与 PTY channel-open 共用全局 8 / 每连接 3 的即时拒绝准入和有限时；底层操作或有界取消清理真正收束前不会提前释放名额。上传正文另限全局 2 / 每连接 1，逻辑在途预算 128 MiB。
- HTTP 服务最多接受 64 个并发 socket；JSON 与下载响应共享 64 个响应 / 64 MiB 的输出预算，Buffer 与名额持有到响应完成或连接关闭，慢读客户端不能无限堆积输出。
- Terminal 保留原始 PTY 字节，以 Base64 块经同源 API 拉取；输出最多保留 1 MiB，输入队列和 drain 等待均有上限。
- 上传单文件最大 16 MiB、一次最多 32 个文件、总量最大 45 MiB；Renderer 与 Node 后端都会校验，后端还拒绝非规范 Base64。下载只允许最大 16 MiB 的普通文件。上传先写临时 `.part`，成功后再 rename。
- 目录列表最多扫描 10,000 项，单名最多 4 KiB、累计元数据最多 4 MiB，并显式报告截断。
- 删除采用 prepare/commit 两阶段确认，只支持单文件、链接或空目录。

JavaScript 引擎和垃圾回收器可能产生无法显式覆盖的短时字符串副本，因此 Web 模式是“本机进程内临时凭据”，不是硬件级秘密隔离。

## Tauri Rust SSH

- `EphemeralConnectionStore` 持有临时 profile 与主机信任。
- Rust `ssh2` / libssh2 直接建立连接，不通过本地 Web 后端或外部进程。
- Windows 构建启用 vendored OpenSSL，使粘贴私钥直接通过 libssh2 内存认证；不会为私钥创建临时磁盘文件。任意文件路径认证不对 Renderer 开放。
- 每个终端 session 通过调用者专属 IPC Channel 发送 Base64 封装的原始 PTY 字节。Rust 最多发送一个未确认的 32 KiB 输出块；Renderer 写入 xterm 后 ACK，15 秒未确认即关闭会话。
- 终端输入单块最大 16 KiB，控制队列、行列数与并发 session 均有限制；Renderer 侧也使用有界输入队列。
- SFTP 每任务最多 32 个文件、单文件 16 MiB、总量 64 MiB，同时最多 4 个任务。上传 worker 有并发上限，并以 `EXCLUSIVE`、`0600` 创建随机临时文件；下载使用调用者专属 Channel，每次仅保留一个未 ACK 的 64 KiB 数据块，15 秒未确认即取消。目录列表同样受 10,000 项、4 KiB 单名和 4 MiB 累计元数据预算约束。
- 一次性阻塞 SSH 操作采用独立准入层：全局最多 8 个、每个临时连接最多 3 个，超限立即返回，不占满 Tauri 阻塞线程池。PTY 写入、resize 与 ACK 使用独立的轻量容量，但共享同一 closing/drain 屏障，长期传输不会饿死终端活动。
- 断开连接会立即阻止新操作，最多等待 5 秒让已准入操作退出，并前后两次关闭该 profile 的 PTY、取消传输、删除 pending host-key，再移除凭据；15 秒周期清理器对空闲过期连接执行同一流程。
- Renderer 只把用户当次输入的认证材料提交给 Rust；Rust 不会把密码、私钥、原始 host-key 或 SSH session 回传给 Renderer。

## Tauri IPC 与 CSP

- `build.rs` 通过 `AppManifest` 声明实际 `ide_*` 命令，并为每条命令生成独立权限。
- `main-capability` 只授权主窗口所需的精确 `allow-ide-*` 列表；不启用 `core:default`、opener、shell 或外部进程权限。
- 生产 CSP 不允许 `unsafe-eval`，禁止 object 与 frame，并且不包含 Vite 的 loopback HTTP/WebSocket 地址。
- 开发服务器地址只存在于独立 `devCsp`，不会进入生产策略。

## MCP HTTP

- `POST /mcp` 使用 Streamable HTTP 的 JSON-RPC 响应形式。
- Bearer token 每次启动生成或从当前进程环境注入；不放入 URL。
- `initialize` 协商固定支持版本；后续请求必须携带相同的 `MCP-Protocol-Version`。
- 浏览器先生成一次性 grant；`ssh_connection_attach` 兑换独立 attachment ID。
- 每个 attachment 具有 scope 和硬过期时间；后续工具必须携带 attachment ID。
- attachment 关闭、过期或 HTTP 请求取消会撤销其在途 exec/SFTP 通道，不关闭共享 SSH transport。
- Agent 不能通过 MCP 提交或读取 SSH 密码。

## 发布前检查

- 使用锁定的 Node.js 24.18.0、npm 12.0.1 与 Rust 1.97.1 执行 `npm run verify`、`npm audit --audit-level=low` 与 `cargo audit --file src-tauri/Cargo.lock`。
- 执行 `powershell -ExecutionPolicy Bypass -File scripts/validate-github-actions.ps1`；工作流只使用完整提交 SHA 固定的 GitHub Actions，且默认权限仅为 `contents: read`。
- Windows 安装包使用 `npm run tauri:icon` 与 `npm run tauri:bundle` 生成，发布前记录产物 SHA-256 并在隔离环境完成安装/卸载回归。
- 在隔离 SSHD 上覆盖 host-key 变化、错误密码、网络中断、PTY resize 与 SFTP 限额。
- 检查 Tauri capability、CSP、更新链与依赖清单；CI 的无签名验证包不得直接作为正式下载发布，正式 EXE、NSIS 与 MSI 必须完成 Authenticode 签名、时间戳和签名状态校验。
- 确认 Node 服务只监听 loopback，Origin/token/version 拒绝路径均有测试。
- 定期轮换真实测试主机密码；不要把生产凭据提交到代码、日志或测试快照。
