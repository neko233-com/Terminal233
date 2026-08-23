# Terminal233 安全说明

## 信任边界

| 边界 | 实现 |
| --- | --- |
| Web macOS | 外部浏览器只访问 `127.0.0.1`；SSH/SFTP 由本地 Node.js `ssh2` 进程执行 |
| rdesktop | Renderer 只调用窄 JSON IPC；纯 Rust `puressh` 直接建立 TCP、SSH、PTY 与 SFTP |
| 运行时隔离 | 两端不共享凭据、主机信任、SSH transport、终端会话或 SFTP 任务 |
| 系统客户端 | 两条链路都不调用 `ssh.exe`、PuTTY、plink、Windows Terminal 或 shell |
| 持久化 | 客户端连接、密码、主机信任与终端仅在进程内；同步服务器只保存 AES-GCM 加密凭据 |

## 主机密钥

Web 和 rdesktop 都先完成 SSH 握手并返回服务器算法与 SHA-256 指纹。未知密钥必须由用户确认；确认 token 与具体 connection ID 绑定且短时有效。确认前不会继续认证。连接期间出现不同指纹会被拒绝，过时的 `ssh-dss`/DSA 主机密钥不会进入信任流程。

Web 探测连接只读取指纹，不携带密码并立即关闭；用户确认后新建认证连接，并再次用保存的指纹验证服务器。rdesktop 使用纯 Rust `puressh` 的 prepare/trust 流程，并在每次新连接时重新验证。

两条链路都使用精确的现代算法策略：密钥交换只允许 Curve25519、ECDH 与 SHA-2 Diffie-Hellman，主机密钥只允许 Ed25519、ECDSA 与 RSA-SHA2，加密只允许 AES-GCM/AES-CTR，MAC 只允许 HMAC-SHA2。SHA-1、`ssh-rsa`、CBC、MD5 与协商结果不在白名单的服务器会直接失败，不做兼容性降级。

## Web localhost 服务

- 固定绑定 `127.0.0.1`，不监听局域网地址。
- 浏览器 API 校验允许的 loopback Origin，不开放 CORS。
- 浏览器会话使用随机 HttpOnly、SameSite=Strict cookie，30 分钟空闲过期。
- 任一 API 返回会话失效后，Renderer 会原子丢弃缓存的会话句柄、关闭当前桌面并要求重新建立页面会话；过期请求不能复用已完成的初始化 Promise，也不能使后来创建的新会话失效。
- 每个页面拥有独立释放令牌；刷新、退出、取消、失败或指纹不匹配都会级联关闭 owner 的终端、授权与 SSH 连接。
- 密码、粘贴私钥与私钥口令不进入 URL、日志或 MCP transcript；认证材料转换为 Buffer 后只由所属连接持有，断开、失败或进程退出时覆盖并关闭连接。
- 连接、确认、授权、attachment 与终端表均有硬数量上限；普通 JSON 请求限制 64 KiB。
- SFTP、exec 与 PTY channel-open 共用全局 8 / 每连接 3 的即时拒绝准入和有限时；底层操作或有界取消清理真正收束前不会提前释放名额。上传正文另限全局 2 / 每连接 1，逻辑在途预算 128 MiB。
- HTTP 服务最多接受 64 个并发 socket；JSON 与下载响应共享 64 个响应 / 64 MiB 的输出预算，Buffer 与名额持有到响应完成或连接关闭，慢读客户端不能无限堆积输出。
- Terminal 保留原始 PTY 字节，以 Base64 块经同源 API 拉取；输出最多保留 1 MiB，输入队列和 drain 等待均有上限。
- 上传单文件最大 16 MiB、一次最多 32 个文件、总量最大 45 MiB；Renderer 与 Node 后端都会校验，后端还拒绝非规范 Base64。下载只允许最大 16 MiB 的普通文件。上传先写临时 `.part`，成功后再 rename。
- 下载在读取远端数据前验证 basename，并以 ASCII fallback 加 RFC 5987 `filename*` 返回 UTF-8 文件名；响应完成、断开或写入失败都会覆盖后端持有的下载 Buffer。
- 目录列表最多扫描 10,000 项，单名最多 4 KiB、累计元数据最多 4 MiB，并显式报告截断。
- 删除采用 prepare/commit 两阶段确认，只支持单文件、链接或空目录。

JavaScript 引擎和垃圾回收器可能产生无法显式覆盖的短时字符串副本，因此 Web 模式是“本机进程内临时凭据”，不是硬件级秘密隔离。

## rdesktop 纯 Rust SSH

- `EphemeralConnectionStore` 持有临时 profile 与主机信任。
- 精确锁定的 `puressh` 直接建立连接，不通过本地 Web 后端或外部进程。
- Terminal233 SSH 与密码学依赖链保持纯 Rust，禁止 `ssh2` / libssh2、OpenSSL / `openssl-sys`、`ring`、`aws-lc-*` 以及原生 C/ASM SSH 或密码学实现。发布前必须检查依赖树，出现这些依赖或 `cc`、CMake、Perl、NASM、nmake 构建路径即失败。Rust 代码仍会通过系统 ABI 使用 Windows TCP、线程、文件和 `ProcessPrng` CSPRNG；这些是操作系统服务，不是 C SSH/密码学运行时。
- 粘贴私钥直接在 `puressh` 进程内认证，不会为私钥创建临时磁盘文件。任意文件路径认证不对 Renderer 开放。
- 每个终端 session 通过调用者专属 IPC Channel 发送 Base64 封装的原始 PTY 字节。Rust 最多发送一个未确认的 32 KiB 输出块；Renderer 写入 xterm 后 ACK，15 秒未确认即关闭会话。
- 终端输入单块最大 16 KiB，控制队列、行列数与并发 session 均有限制；Renderer 侧也使用有界输入队列。
- SFTP 每任务最多 32 个文件、单文件 16 MiB、总量 64 MiB，同时最多 4 个任务。上传与下载都使用调用者专属 IPC Channel，避免全局事件监听竞态；上传 worker 有并发上限，并以 `EXCLUSIVE`、`0600` 创建随机临时文件，Renderer 会拒绝重复、乱序或越界的任务状态；下载每次仅保留一个未 ACK 的 64 KiB 数据块，15 秒未确认即取消。目录列表同样受 10,000 项、4 KiB 单名和 4 MiB 累计元数据预算约束。
- 一次性阻塞 SSH 操作采用独立准入层：全局最多 8 个、每个临时连接最多 3 个，超限立即返回，不占满 rdesktop 事件线程。PTY 写入、resize 与 ACK 使用独立的轻量容量，但共享同一 closing/drain 屏障，长期传输不会饿死终端活动。
- 断开连接会立即阻止新操作，最多等待 5 秒让已准入操作退出，并前后两次关闭该 profile 的 PTY、取消传输、删除 pending host-key，再移除凭据；15 秒周期清理器对空闲过期连接执行同一流程。
- Renderer 只把用户当次输入的认证材料提交给 Rust；Rust 不会把密码、私钥、原始 host-key 或 SSH session 回传给 Renderer。

## rdesktop IPC 与 CSP

- rdesktop IPC 只接受固定命令名与受限 JSON 字段；终端、上传和下载事件使用调用者专属 channelId，不使用全局广播。
- 生产前端不允许 `unsafe-eval`，禁止 object 与 frame；rdesktop 自定义协议只加载项目构建出的静态 dist 目录。
- rdesktop 窗口与前端 IPC 不提供 shell、opener 或外部进程入口。

## Go 同步服务器

- 注册接口必须提交官方 token；官方 token 不写入数据文件、不返回给客户端日志。
- 用户密码使用 scrypt 哈希；SSH 密码使用 AES-256-GCM 加密，master key 只从部署环境注入。
- 保存 SSH 凭据前，服务器会在超时内完成真实 SSH 握手、密码认证和 `true` 命令；失败响应不会写入账号记录。
- 每个用户最多 1000 条账号；JSON 接口统一发送 `Cache-Control: no-store`，管理员页面不显示 SSH 密码。
- 默认管理员为 root/root 仅用于首次本机启动；公网部署必须修改密码并通过 HTTPS 反向代理保护。

## MCP HTTP

- `POST /mcp` 使用 Streamable HTTP 的 JSON-RPC 响应形式。
- Bearer token 每次启动生成或从当前进程环境注入；不放入 URL。
- `initialize` 协商固定支持版本；后续请求必须携带相同的 `MCP-Protocol-Version`。
- 浏览器先生成一次性 grant；`ssh_connection_attach` 兑换独立 attachment ID。
- 每个 attachment 具有 scope 和硬过期时间；后续工具必须携带 attachment ID。
- attachment 关闭、过期或 HTTP 请求取消会撤销其在途 exec/SFTP 通道，不关闭共享 SSH transport。
- Agent 不能通过 MCP 提交或读取 SSH 密码。

## 发布前检查

- 使用锁定的 Node.js 24.18.1、npm 12.0.2 与 Rust 1.97.1 执行 `npm run verify`、`npm audit --audit-level=low`，并审计 Terminal233、Web 启动器与 Go server 依赖。
- 仓库级 GitHub Actions 保持禁用，版本库不保留自动 workflow；所有发布验证和打包都从干净且已推送的 `main` 手工执行。
- Windows 资产必须直接运行 `powershell.exe -File scripts/package-windows-release.ps1` 生成，禁止由 npm bootstrap 正式打包；脚本在首次调用 Git/Node/npm 前以独立 SHA-256 锚定仓库外的 builder toolchain lock，并把全部工具身份写入 manifest。脚本只删除当前版本的精确旧产物、拒绝 reparse point、精确选择新产物、记录 SHA-256，并在发布前检查 Authenticode、时间戳和预期证书指纹。
- 第一阶段工具 lock 尚未绑定 rustdoc、rdesktop/WebView native binding 与 `cl/link/lib/rc/mt`，因此正式打包、回执签名和发布强制 fail-closed；只有 `-AllowUnsigned` 本机验证可运行，补齐全部 identity 前不得提供绕过。
- 独立 Web ZIP 只包含签名项目启动器、固定且经官方哈希与 OpenJS 签名验证的 Node.js、Web 构建和最小纯 JavaScript `ssh2` 依赖。启动器清除 `NODE_OPTIONS`、`NODE_PATH`、外部 MCP token 等注入环境，在 Node 权限模式下运行，不授予子进程或原生扩展能力，并以嵌入启动器的 manifest 哈希验证全部 payload 及额外文件。无签名 ZIP 只做静态本机校验，验证器不会执行其中的 EXE。
- 在隔离 SSHD 上覆盖 host-key 变化、错误密码、网络中断、PTY resize 与 SFTP 限额。
- 检查 rdesktop IPC、CSP、降级策略与依赖清单；正式分发的 Terminal233 主程序与 Web 启动器必须完成 Authenticode 签名、RFC 3161 时间戳，以及签名策略中的 Thumbprint、Subject、SPKI 和版本窗口校验。无签名输出一律 `publishable=false`。
- 稳定版由受控签名机在 VM 启动前预发 256-bit challenge；一次性干净 Windows x64 VM 验证 NSIS/MSI 安装、同格式升级或重装、降级阻止、ARP 卸载命令、卸载残留、全部实际安装 PE 签名以及两条内置 SSH 运行时自检。VM 不持有生产私钥，未签回执返回受控机器复验后再签名。
- 回执签名机使用与 manifest 相同、由独立 SHA-256 锚定的 builder toolchain lock 解析 `git.exe`，拒绝 PATH 重新解析、工具身份漂移和隐藏索引标志，并把策略的同一次解析字节绑定到 `HEAD` blob。私钥只对禁用继承、限制为签名身份/SYSTEM/Administrators 且单硬链接的私有本地副本操作；发行目录中的替换失败时从独立受保护的未签回执恢复。
- GitHub 发布器必须确认严格 manifest、验收回执、`HEAD`、`origin/main` 与预先存在的 cryptographically signed annotated tag 指向同一提交，并以受控 OpenPGP trust policy 或 SSH allowedSigners 加精确 signer fingerprint 执行 `git verify-tag`；发布器绝不自动创建 tag。Actions 必须禁用，immutable releases 必须启用。草稿与公开发布后都会重新下载全部 Release 资产、逐一核对哈希并重跑签名 Web 自检，最终还验证 release 与逐资产 GitHub attestation。
- 确认 Node 服务只监听 loopback，Origin/token/version 拒绝路径均有测试。
- 定期轮换真实测试主机密码；不要把生产凭据提交到代码、日志或测试快照。
