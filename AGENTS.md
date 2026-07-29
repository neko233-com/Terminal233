# NekoSSH Product Boundaries

## 两个独立入口

- **Web（独立浏览器工作台）**：本机 `localhost` 后端 + 外部浏览器。必须保留 macOS 伪桌面体验：桌面、Dock、Terminal、Finder、窗口、拖拽和右键菜单。
- **Tauri v2（传统 SSH IDE）**：只做连接列表、连接过程、终端、文件树和轻量状态面板。禁止使用 macOS 伪桌面作为 Tauri 主界面。

## SSH 技术边界

- Web macOS 的 SSH/SFTP 必须由随项目启动的 Node.js 本地后端内置 `ssh2`（ssh2js）实现；浏览器只通过同源 localhost API 使用它。
- Tauri v2 必须直接使用 Rust `ssh2` / libssh2 进程内实现 SSH、PTY 和 SFTP；不得通过 Web 后端中转。
- 两条链路都禁止调用或依赖 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他外部 SSH 客户端。
- Web 与 Tauri 可以共享数据模型和测试向量，不共享 SSH 运行时、连接对象、终端会话或凭据。

## 精简与安全

- 不做 Vault、云同步、密码锁、商品化设置页或无关功能；SSH 凭据与主机信任只保留在进程内，断开或退出即清空。
- 所有 UI 文案优先中文；连接过程必须显示中文状态与可读失败原因。
- Tauri 与 Web 的代码可以复用 SSH 内存会话核心，但界面和入口必须独立。
- 连接列表要支持搜索、分组、右键菜单、拖拽排序；文件区要支持拖拽上传、右键操作和显式删除确认。
- 优先少而完整的功能。每个操作必须有即时反馈、键盘可用性和 `prefers-reduced-motion` 降级。
