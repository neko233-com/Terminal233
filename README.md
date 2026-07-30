# NekoSSH

NekoSSH 提供两个完全独立的 SSH 入口：

| 入口 | 用途 | SSH 实现 |
| --- | --- | --- |
| Web macOS | 外部本地浏览器中的 macOS 伪桌面、Terminal、Finder、拖拽与右键操作 | Node.js 本地后端内置 `ssh2`（ssh2js） |
| Tauri v2 | 传统三栏 SSH IDE：连接列表、远端文件、终端、会话状态 | Rust `ssh2` / libssh2 进程内实现 |

两条链路拥有各自的进程内凭据、主机信任、SSH 连接、PTY 与 SFTP 状态，互不共享或中转。它们都不调用 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他系统 SSH 客户端；Vite 只负责界面渲染。

## 产品边界

- Web macOS 必须在系统浏览器中运行，不嵌入 Tauri WebView。
- Tauri 只提供传统 SSH IDE，不包含 macOS 伪桌面、长期凭据存储、云同步或密码锁。
- SSH 密码、内嵌私钥、临时主机信任只保存在当前进程内；断开或退出后清理。
- 首次连接必须确认服务器 SHA-256 指纹；变化的主机密钥会阻止连接。
- 文件上传默认不覆盖。删除只允许单文件、链接或空目录，并要求显式确认。

详细规范见 [AGENTS.md](AGENTS.md)，威胁边界见 [安全说明](docs/SECURITY.md)。

## 运行

先安装 Node.js 24.18.1（npm 12.0.2）与 Rust 1.97.1，并准备 Tauri v2 对应平台依赖。Windows 原生构建还需要 Visual Studio C++ Build Tools、完整 Perl（含 `Locale::Maketext::Simple`）与 NASM，用于编译内存私钥认证所需的 vendored OpenSSL。仓库中的 `.nvmrc` 和 `rust-toolchain.toml` 固定了应用运行时版本，手工发布脚本会显式检查这些原生构建前置条件。

```powershell
npm ci

# 外部浏览器 Web macOS + Node ssh2js 后端 + localhost MCP
npm run web

# 传统 Tauri v2 Rust SSH IDE
npm run tauri:dev
```

`npm run web` 默认启动：

- Web GUI：默认 `http://127.0.0.1:5173/`；端口占用时自动选择 `5174-5199`
- 本地 API / MCP：`http://127.0.0.1:7331/`

启动脚本会生成一次性的 MCP Bearer token 并显示在当前控制台，不写入项目或持久凭据存储。浏览器连接后，可在 Agent 窗口生成一次性授权，再由 Agent 调用 `ssh_connection_attach`。

## 构建与测试

完整验证可在命令行完成，不需要浏览器自动化或桌面操控：

```powershell
npm ci
npm run verify
npm run audit:deps
```

`npm run verify` 会构建并检查两个独立 Vite mode，运行前端、Node 与 Rust 测试，并执行 Rust 格式和 Clippy 校验。构建验证器会阻止任一生产包携带另一入口的主界面代码。

## 手工打包 Windows 双运行时

仓库已禁用 GitHub Actions。Windows 发行资产必须从干净且已推送的 `main` 手工验证和打包：

```powershell
# 有本地 Authenticode 配置的正式发行
npm run release:windows '--' `
  -TauriSigningConfig C:\secure\nekossh-signing.json `
  -ExpectedSignerThumbprint <证书指纹>

# 没有签名条件时，仅生成不可发布的本机验证产物
npm run release:windows '--' -AllowUnsigned
```

脚本会执行锁定依赖安装、Web/Tauri 双构建、测试、依赖审计，并生成以下两套 Windows 入口：

- Tauri v2 原生 IDE：NSIS 与简体中文 MSI。
- 独立 Web macOS：签名原生启动器、固定且经官方签名验证的 Node.js、静态前端及最小 `ssh2` 依赖组成的便携 ZIP。

NSIS 面向当前用户，MSI 面向管理员部署；两者是独立安装通道，不支持跨格式直接覆盖，切换前应先卸载。

同时生成第三方许可、SHA-256 清单、发行说明和提交绑定 manifest。`tauri:bundle` 中的 `--ci` 只是 Tauri CLI 的非交互构建参数，不会调用 GitHub Actions。

正式分发必须使用版本控制签名策略允许的可信 Authenticode 证书，为真正分发的 Tauri 主程序、NSIS、MSI 与 Web 启动器签名并加 RFC 3161 时间戳；内置 `node.exe` 还必须匹配固定官方 SHA-256 及 OpenJS 有效时间戳签名。仓库不存放签名私钥。任何无签名输出都标记为 `publishable=false`，发布器会无条件拒绝。

稳定版发布前还必须由受控签名机预发一次性挑战，在一次性干净 Windows x64 VM 中执行 NSIS/MSI 安装、同格式升级或重装、降级阻止、卸载、ARP/残留检查、已安装 PE 签名与两套运行时自检。VM 生成未签回执，受控签名机复验后再签名 `WINDOWS_SMOKE_RECEIPT.psd1`。`npm run release:publish '--' -ReleaseDirectory <输出目录>` 会验证 manifest、annotated tag、Actions 禁用状态、不可变 Release 策略、草稿与公开资产、重新下载哈希及 GitHub attestations。发行文件属于 GitHub Release assets，不进入 GitHub Packages Registry。完整流程见 [手工发布说明](docs/RELEASING.md)。

## 安全限制

- Node.js 无法保证垃圾回收前的所有字符串副本可原地清零；Web 密码仍只存在于本机进程内，不持久化、不记录日志。
- localhost 服务固定绑定 `127.0.0.1`，校验浏览器 Origin，并使用 HttpOnly、SameSite=Strict 的临时会话 cookie。
- MCP 使用独立 Bearer token、一次性 grant、短时 attachment ID 与 scope；Agent 不直接接收 SSH 密码。
- Tauri PTY 通过 IPC Channel 传递原始二进制字节，并以逐块 ACK、超时和有界队列施加背压；不会把终端流提前转成普通文本。
- Tauri SFTP 单文件上限 16 MiB、单任务 64 MiB、每任务最多 32 个文件；下载同样通过逐块 ACK 限制内存占用。
- 高价值主机首次连接时，仍应通过可信渠道比对指纹。

## 设计参考

[macos.kimi.site](https://macos.kimi.site/) 仅作为桌面隐喻与交互研究参考。NekoSSH 的界面和代码保持原创，不复制第三方脚本、品牌元素或未授权素材。
