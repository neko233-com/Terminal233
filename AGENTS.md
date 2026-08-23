# Terminal233 Product Boundaries

## 产品入口

- **Web（独立浏览器工作台）**：本机 `localhost` 后端 + 外部浏览器。保留 macOS 伪桌面体验：桌面、Dock、Terminal、Finder、窗口、拖拽和右键菜单。
- **Terminal233 桌面端**：位于 `terminal233/`，使用锁定版本的 `rdesktop` 与纯 Rust `puressh`，提供连接列表、连接过程、终端、文件树和轻量状态面板。桌面端不使用 Web 伪桌面作为主界面。
- **Terminal233 Server**：位于 `terminal233-server/`，只发布 Linux 服务端；用户可以自行部署自己的 SSH 密码保存服务器。

## 平台与发布范围

- 桌面端正式支持 Windows `x86_64-pc-windows-msvc` 与 `aarch64-pc-windows-msvc`，安装目录默认为 `C:\App\Terminal233`。
- 32 位 Windows、`i686`、`i386` 及其他 32 位桌面目标明确不在范围内。产品文档中的“Windows x86”一律指 x86_64，不得新增 32 位构建、安装包、测试或发布资产。
- 服务端只考虑 Linux `amd64` 与 `arm64`；不发布 Windows 服务端。
- 发布、更新清单和平台资产必须使用 `scripts/package-terminal233-release.ps1` 及其配套脚本生成。缺少目标工具链时必须失败并报告，不得伪造或改名为其他架构的资产。
- 桌面端自动检查更新只信任项目发布的更新清单和对应校验值；版本比较、平台筛选和安装包命名必须保持 x86_64/arm64 一致。

## rdesktop 集成边界

- `terminal233/Cargo.toml` 中的 `rdesktop` 版本和 git revision 必须精确锁定；升级前先验证 Windows x86_64 与 ARM64 构建路径及纯 Rust 依赖约束。
- 实际上游源码由 Cargo git dependency 管理；仓库内 `rdesktop/README.md` 与 `rdesktop/AGENTS.md` 是 Terminal233 的集成说明和操作约束，不是上游源码副本。
- 如果必须修改上游实现，应提交上游变更或更新锁定 revision，并在 Terminal233 中补充验证记录；不要直接把 Cargo 缓存当作可交付源码目录。

## SSH 技术边界

- Web 的 SSH/SFTP 必须由随项目启动的 Node.js 本地后端内置 `ssh2` 实现；浏览器只通过同源 localhost API 使用它。
- 桌面端必须直接使用精确锁定的纯 Rust `puressh` 进程内实现 SSH、PTY 和 SFTP，不得通过 Web 后端中转。
- 桌面端 SSH 实现及其密码学、传输依赖链禁止使用 `ssh2` / libssh2、OpenSSL / `openssl-sys`、`ring`、`aws-lc-*`，也禁止任何原生 C/ASM SSH 或密码学实现，以及需要 `cc`、CMake、Perl、NASM、nmake 的构建路径。
- 两条链路都禁止调用或依赖 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他外部 SSH 客户端。
- Web 与桌面端可以共享数据模型和测试向量，但不共享 SSH 运行时、连接对象、终端会话或凭据。

## 精简与安全

- 不做 Vault、云同步、密码锁、商品化设置页或无关功能；SSH 凭据与主机信任只保留在进程内，断开或退出即清空。
- 所有 UI 文案优先中文；连接过程必须显示中文状态与可读失败原因。
- 连接列表要支持搜索、分组、右键菜单、拖拽排序；文件区要支持拖拽上传、右键操作和显式删除确认。
- 优先少而完整的功能。每个操作必须有即时反馈、键盘可用性和 `prefers-reduced-motion` 降级。

## Git 开发规范

- 所有后续开发、修复、提交与推送只在 `main` 分支进行；不得继续使用或新建 `codex/*` 开发分支。
- 保留用户未提交的本地改动；提交时只暂存当前任务明确涉及的文件。
- 每次新增构建、测试、打包或本机运行产物时都必须同步检查并维护 `.gitignore`；禁止把依赖目录、缓存、日志、临时目录、安装包、发布候选、凭据或签名材料提交到 Git。
- 完成可交付改动后必须提交并推送 `main` 上本任务的全部改动；推送前后确认工作树干净且 `HEAD == origin/main`。

## Windows 开发机交付

- 只要 Windows 版本通过本轮验证，就必须使用命令行构建并静默安装最新的 Terminal233 到 `C:\App\Terminal233`，再核验已安装版本；禁止依赖 GUI 或电脑操控完成安装。
- 开发机安装以当前 `main` 的最新已验证提交为准。若缺少可信 Authenticode 证书，可以安装明确标记为本机验证用途的 unsigned 构建，但禁止把它上传为正式 GitHub Release。
- GitHub Actions 必须保持禁用，仓库不得新增 `.github/workflows`；Windows 安装包与 GitHub Release 资产均走文档约定的手工流程。
