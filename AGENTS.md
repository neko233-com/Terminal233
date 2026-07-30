# NekoSSH Product Boundaries

## 两个独立入口

- **Web（独立浏览器工作台）**：本机 `localhost` 后端 + 外部浏览器。必须保留 macOS 伪桌面体验：桌面、Dock、Terminal、Finder、窗口、拖拽和右键菜单。
- **Tauri v2（传统 SSH IDE）**：只做连接列表、连接过程、终端、文件树和轻量状态面板。禁止使用 macOS 伪桌面作为 Tauri 主界面。

## SSH 技术边界

- Web macOS 的 SSH/SFTP 必须由随项目启动的 Node.js 本地后端内置 `ssh2`（ssh2js）实现；浏览器只通过同源 localhost API 使用它。
- Tauri v2 必须直接使用精确锁定的纯 Rust `puressh` 进程内实现 SSH、PTY 和 SFTP；不得通过 Web 后端中转。
- Tauri SSH 实现及其密码学、传输依赖链禁止使用 `ssh2` / libssh2、OpenSSL / `openssl-sys`、`ring`、`aws-lc-*`，也禁止任何原生 C/ASM SSH 或密码学实现，以及需要 `cc`、CMake、Perl、NASM、nmake 的构建路径。纯 Rust 代码直接调用操作系统 TCP、线程、文件和 CSPRNG API 不视为 C SSH 运行时。
- 两条链路都禁止调用或依赖 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他外部 SSH 客户端。
- Web 与 Tauri 可以共享数据模型和测试向量，不共享 SSH 运行时、连接对象、终端会话或凭据。

## 精简与安全

- 不做 Vault、云同步、密码锁、商品化设置页或无关功能；SSH 凭据与主机信任只保留在进程内，断开或退出即清空。
- 所有 UI 文案优先中文；连接过程必须显示中文状态与可读失败原因。
- Tauri 与 Web 的代码可以复用 SSH 内存会话核心，但界面和入口必须独立。
- 连接列表要支持搜索、分组、右键菜单、拖拽排序；文件区要支持拖拽上传、右键操作和显式删除确认。
- 优先少而完整的功能。每个操作必须有即时反馈、键盘可用性和 `prefers-reduced-motion` 降级。

## Git 开发规范

- `codex/stable-dual-ssh-runtime` 合并完成后，所有后续开发、修复、提交与推送只在 `main` 分支进行。
- 开始修改前必须先确认当前分支为 `main`；不得继续使用或新建 `codex/*` 开发分支。
- 保留用户未提交的本地改动；提交时只暂存当前任务明确涉及的文件。
- 每次新增构建、测试、打包或本机运行产物时都必须同步检查并维护 `.gitignore`；禁止把依赖目录、缓存、日志、临时目录、安装包、发布候选、凭据或签名材料提交到 Git。
- 完成可交付改动后必须提交并推送 `main` 上本任务的全部改动；推送前后确认工作树干净且 `HEAD == origin/main`。

## Windows 开发机交付

- 只要 Windows 版本通过本轮验证，就必须使用命令行构建并静默安装最新的 NekoSSH 到开发机，再核验已安装版本；禁止依赖 GUI 或电脑操控完成安装。
- 开发机安装以当前 `main` 的最新已验证提交为准。若缺少可信 Authenticode 证书，可以安装明确标记为本机验证用途的 unsigned 构建，但禁止把它上传为正式 GitHub Release。
- GitHub Actions 必须保持禁用，仓库不得新增 `.github/workflows`；Windows 安装包与 GitHub Release 资产均走文档约定的手工流程。
