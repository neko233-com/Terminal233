# NekoSSH 安全说明

## 信任边界

| 边界 | 实现 |
| --- | --- |
| Web macOS | 外部浏览器只访问 `127.0.0.1`；SSH/SFTP 由本地 Node.js `ssh2` 进程执行 |
| Tauri v2 | Renderer 只调用窄 IPC；Rust `ssh2` 直接建立 TCP、SSH、PTY 与 SFTP |
| 系统客户端 | 两条链路都不调用 `ssh.exe`、PuTTY、plink、Windows Terminal或 shell |
| 持久化 | 不创建 Vault；连接、密码、主机信任、终端与 Agent grant 仅保存在进程内 |

## 主机密钥

Web 和 Tauri 都先完成 SSH 握手并返回服务器算法与 SHA-256 指纹。未知密钥必须由用户确认；确认 token 与具体 connection ID 绑定且短时有效。确认前不会继续认证。连接期间出现不同指纹会被拒绝。

Web 探测连接只读取指纹，不携带密码并立即关闭；用户确认后新建认证连接，并再次用保存的指纹验证服务器。Tauri 使用 Rust `ssh2` 的 prepare/trust 流程，并在每次新连接时重新验证。

## Web localhost 服务

- 固定绑定 `127.0.0.1`，不监听局域网地址。
- 浏览器 API 校验允许的 loopback Origin，不开放 CORS。
- 浏览器会话使用随机 HttpOnly、SameSite=Strict cookie，30 分钟空闲过期。
- 每个页面拥有独立释放令牌；刷新、退出、取消、失败或指纹不匹配都会级联关闭 owner 的终端、授权与 SSH 连接。
- 密码不进入 URL、日志或 MCP transcript；断开时覆盖持有密码的 Buffer 并关闭连接。
- 连接、确认、授权、attachment 与终端表均有硬数量上限；普通 JSON 请求限制 64 KiB。
- Terminal 输出最多保留 1 MiB；关闭后清空，已关闭会话短时回收。
- 上传单文件最大 16 MiB、最多 32 个文件；临时 `.part` 成功后再 rename。
- 删除采用 prepare/commit 两阶段确认，只支持单文件、链接或空目录。

JavaScript 引擎和垃圾回收器可能产生无法显式覆盖的短时字符串副本，因此 Web 模式是“本机进程内临时凭据”，不是硬件级秘密隔离。

## Tauri Rust SSH

- `EphemeralConnectionStore` 持有临时 profile 与主机信任。
- Rust `ssh2` / libssh2 直接建立连接，不通过本地 Web 后端或外部进程。
- 终端每个 session 使用有界控制队列；输入块、行列数、并发 session 均有限制。
- SFTP 上传/下载有文件、总量、并发 job 与 worker 限制。
- 断开连接会先关闭该 profile 的 PTY、取消其传输、删除 pending host-key，再移除凭据。
- Renderer 不接收密码、私钥、原始 host-key 或 Rust SSH session。

## MCP HTTP

- `POST /mcp` 使用 Streamable HTTP 的 JSON-RPC 响应形式。
- Bearer token 每次启动生成或从当前进程环境注入；不放入 URL。
- `initialize` 协商固定支持版本；后续请求必须携带相同的 `MCP-Protocol-Version`。
- 浏览器先生成一次性 grant；`ssh_connection_attach` 兑换独立 attachment ID。
- 每个 attachment 具有 scope 和硬过期时间；后续工具必须携带 attachment ID。
- attachment 关闭、过期或 HTTP 请求取消会撤销其在途 exec/SFTP 通道，不关闭共享 SSH transport。
- Agent 不能通过 MCP 提交或读取 SSH 密码。

## 发布前检查

- 在隔离 SSHD 上覆盖 host-key 变化、错误密码、网络中断、PTY resize 与 SFTP 限额。
- 检查 Tauri capability、CSP、签名、更新链与依赖 SBOM。
- 确认 Node 服务只监听 loopback，Origin/token/version 拒绝路径均有测试。
- 定期轮换真实测试主机密码；不要把生产凭据提交到代码、日志或测试快照。
