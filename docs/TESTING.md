# Terminal233 测试说明

当前 Terminal233 发布验证入口：

```powershell
corepack npm run typecheck
corepack npm run test:ui
corepack npm run server:go:test
corepack npm run terminal233:package:windows
corepack npm run terminal233:package:server:linux
corepack npm run terminal233:package:server:linux:arm64
```

完整发布包使用 `corepack npm run terminal233:package:release`，发布前由 `scripts/publish-terminal233-release.ps1` 手工上传；客户端更新清单必须覆盖 Windows x86_64/ARM64 和 Linux amd64/arm64 四个资产。

## 自动验证

核心稳定性与安全边界可完全通过 CLI 验证；本流程不需要浏览器自动化、桌面操控或截图。

```powershell
npm ci
node --check server/ssh2js-server.mjs
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-release-signing-security.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-git-source-integrity.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-release-manifest-source-security.ps1
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-release-toolchain-lock-security.ps1 `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256>
npm run verify
npm run audit:deps

# 不可发布的本机打包验证
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-windows-release.ps1 `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256> `
  -AllowUnsigned
```

仓库禁用 GitHub Actions。手工发布环境固定使用 Node.js 24.18.1、npm 12.0.2 与 Rust 1.97.1（`.nvmrc`、`packageManager`、`rust-toolchain.toml`）。
Windows 上运行 Rust/rdesktop 验证仍需要锁定的 Visual Studio Build Tools 与 Windows SDK 作为 MSVC 链接工具，但 Terminal233 SSH 依赖链必须保持纯 Rust，不得要求 Perl、NASM、nmake、CMake 或 C/ASM 编译器来构建 SSH/密码学依赖。
builder lock 中的 rustc/Cargo 必须显式指向 `rustup which --toolchain 1.97.1-x86_64-pc-windows-msvc rustc|cargo` 返回的实际二进制；禁止锁定 `%USERPROFILE%\.cargo\bin` 中字节相同的 rustup proxy。
工具身份门禁当前是第一阶段：未覆盖 rustdoc、rdesktop/WebView native binding 与 MSVC/SDK 的 `cl/link/lib/rc/mt`，所以脚本只允许 `-AllowUnsigned` 验证，正式打包、回执签名和发布按设计 fail-closed。

`npm run verify` 会执行 Terminal233 rdesktop renderer 与 Web macOS renderer 的生产构建、运行时包隔离检查、前端、Node 与 Go server 测试，以及 Rust fmt、Clippy 和测试。测试数量随覆盖面增长，不在文档中写死。

## 本地完整验收

本轮 SSH 验收使用隔离的真实 SSHD 夹具；它会覆盖密码认证、加密私钥认证、错误凭据、host-key 信任与拒绝、PTY 请求/resize/输入/输出/关闭、SFTP 列表、上传、下载、改名、目录创建与删除、大小限制、原子覆盖和权限错误。运行：

```powershell
corepack npm run verify
```

其中 `npm run test:native-ssh` 会启动一次性 SSHD，并输出 `ptyRequests`、`shellSessions`、`windowChanges`、`sftpSessions` 与上传字节数；必须看到 `status:passed`。纯 Rust SSH 依赖检查由 `npm run test:pure-rust-ssh` 执行，禁止 `ssh2`、libssh2、OpenSSL、`ring`、`aws-lc-*` 和原生 C/ASM SSH/密码学链路。

桌面视觉验收使用 `npm run web` 启动 Web 工作台，在 1280px 桌面视口和 390×844 移动视口分别检查：连接卡片、表单、按钮和中文状态可读；移动视口不得出现横向滚动；浏览器控制台不得出现 warning/error。页面标题必须为 `Terminal233`。

打包验收使用以下命令：

```powershell
corepack npm run terminal233:package:windows
corepack npm run terminal233:package:server:linux
corepack npm run terminal233:package:server:linux:arm64
corepack npm run terminal233:package:windows:arm64
```

Windows x86_64 包应解压并静默安装到 `C:\App\Terminal233`，然后检查 `Terminal233.exe --version`、`Terminal233.exe --self-test` 和 `dist\index.html` 标题。Windows ARM64 构建需要本机安装对应的 Visual C++ Build Tools/Windows SDK；缺少 `link.exe` 时必须明确记录为环境阻塞，不得伪造 ARM64 包。Linux server 包只发布 amd64 与 arm64 两种架构。

`scripts/package-windows-release.ps1` 从全新的 Cargo target 构建并精确收集当前版本 NSIS/MSI，同时生成独立 Web macOS Windows 便携 ZIP、第三方许可、`SHA256SUMS.txt` 与提交绑定 manifest。正式打包与发布禁止以 npm script 作为入口；必须直接调用 PowerShell，并在任何 Git/gh/Node/npm 调用前以独立 SHA-256 锚定仓库外的 builder toolchain lock。正式构建还必须传入本机 Tauri 签名配置和策略允许的证书指纹，并要求真正分发的 Tauri 主程序、NSIS、MSI 与 Web 启动器均为有效时间戳签名。`-AllowUnsigned` 仅允许本机静态打包验证，manifest 固定为 `publishable=false`，且验证器不会执行未签名 EXE。详见 [手工发布说明](RELEASING.md)。

私有仓库的本机 unsigned 验证可在外部完成 `git fetch origin main` 后追加
`-UseExistingOriginMainForUnsigned`；它不向隔离进程注入凭据，只复验现有
`origin/main`，且不能用于任何正式发布。

稳定版还要求受控机器先生成一次性 nonce，再在一次性干净 Windows x64 VM 中以管理员身份执行 `scripts/test-windows-release-installers.ps1`。验收覆盖 NSIS 与 MSI 的安装、同格式升级、重装、降级阻止、ProductVersion、ARP 注册/卸载命令、卸载残留，全部实际安装 PE 与卸载器签名，Tauri 纯 Rust `puressh` 自检，以及签名 Web 便携包的 payload/Node/同源 API 自检。VM 只生成未签回执；受控签名机在七天内复验 nonce、镜像 ID、脚本/策略及全部资产哈希后再签名。

自动测试应覆盖：

- rdesktop IPC、生产/开发 CSP 分离，以及无 opener、shell、外部进程权限。
- 纯 Rust `puressh` 的 host-key prepare/trust/mismatch、认证前后重新握手、现代算法精确白名单、旧 SHA-1/`ssh-rsa`/CBC/MD5 拒绝、PTY 边界与 SFTP 输入限制；依赖树必须拒绝 `ssh2`、libssh2、OpenSSL、`ring`、`aws-lc-*`、原生 C/ASM SSH 或密码学实现及其构建链，并精确校验启用的 `puressh` features。Windows `ProcessPrng` 等系统 API 调用不属于 C SSH 运行时。
- rdesktop PTY 原始字节 Channel、逐块 ACK、ACK 超时、输入背压与断开清理。
- rdesktop SFTP 的上传边界、下载 started/chunk/completed 顺序、逐块 ACK、任务取消与大小/并发上限。
- rdesktop 阻塞 SSH 操作的全局/单连接准入上限、断开屏障、有界 drain、空闲过期回收与迟到资源自清理。
- Web API 的 Origin、cookie owner、页面会话失效重建、连接 TTL、密码/加密私钥认证、现代算法无降级、UTF-8 下载文件名、原始 PTY 字节、terminal output 上限、输入背压与两阶段删除。
- MCP token、版本协商、一次性 grant、attachment 隔离、scope 与硬 TTL。
- 页面刷新/退出清理、活跃连接续期、过期连接不复活、共享 SSH transport 的通道错误隔离。
- Terminal233 与 Web 两种 Vite mode 分别构建，不携带另一入口的主界面 chunk。
- Web 便携启动器拒绝 payload 哈希、额外文件、路径、官方 Node 哈希或签名异常，并在 Node 权限模式下完成无浏览器同源自检。

## 隔离 SSHD 验收

需要验证真实协议互操作时，只使用授权的隔离 SSHD，并在测试结束后清理远端数据：

1. 分别从 Web 与 Terminal233 完成首次指纹确认、密码认证、加密私钥认证和错误凭据回退；不要复用两端的连接或凭据状态。另用只开放 SHA-1/`ssh-rsa`/CBC/MD5 的隔离实例确认两端都拒绝协商，且不会降级。
2. 运行持续输出、Unicode、全屏 TUI 与 PTY resize 场景，确认断流、背压或关闭都能得到明确反馈。
3. 覆盖 SFTP 列表、上传、下载、改名、空目录删除、显式删除确认、大小上限、取消和网络中断。
4. Web 额外验证 MCP grant → attachment → `ssh_exec`，以及错误 Origin、token、协议版本和过期 attachment 的拒绝路径。
5. 断开后确认终端、传输、attachment、待确认主机密钥与进程内凭据全部失效。

不得把生产密码、私钥或主机数据写入仓库、日志、测试快照或持久测试夹具。
