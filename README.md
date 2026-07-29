# NekoSSH

NekoSSH 提供两个完全独立的 SSH 入口：

| 入口 | 用途 | SSH 实现 |
| --- | --- | --- |
| Web macOS | 外部本地浏览器中的 macOS 伪桌面、Terminal、Finder、拖拽与右键操作 | Node.js 本地后端内置 `ssh2`（ssh2js） |
| Tauri v2 | 传统三栏 SSH IDE：连接列表、远端文件、终端、会话状态 | Rust `ssh2` / libssh2 进程内实现 |

两条链路都不调用 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他外部 SSH 客户端。Vite 只负责界面渲染，不能建立 SSH 连接。

## 产品边界

- Web macOS 必须在系统浏览器中运行，不嵌入 Tauri WebView。
- Tauri 只提供传统 SSH IDE，不包含 macOS 伪桌面、Vault、云同步或密码锁。
- SSH 密码、内嵌私钥、临时主机信任只保存在当前进程内；断开或退出后清理。
- 首次连接必须确认服务器 SHA-256 指纹；变化的主机密钥会阻止连接。
- 文件上传默认不覆盖。删除只允许单文件、链接或空目录，并要求显式确认。

详细规范见 [AGENTS.md](AGENTS.md)，威胁边界见 [安全说明](docs/SECURITY.md)。

## 运行

先安装 Node.js、Rust 1.96 与 Tauri v2 对应平台依赖。

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

启动脚本会生成一次性的 MCP Bearer token 并显示在当前控制台，不写入项目或 Vault。浏览器连接后，可在 Agent 窗口生成一次性授权，再由 Agent 调用 `ssh_connection_attach`。

## 构建与测试

```powershell
# Tauri renderer
npm run build

# Web macOS renderer
npm run web:build

npm test
cargo test --manifest-path src-tauri/Cargo.toml
npm audit
```

Tauri 与 Web 使用不同 Vite mode，生产构建会分别裁剪另一入口的界面代码。

## 安全限制

- Node.js 无法保证垃圾回收前的所有字符串副本可原地清零；Web 密码仍只存在于本机进程内，不持久化、不记录日志。
- localhost 服务固定绑定 `127.0.0.1`，校验浏览器 Origin，并使用 HttpOnly、SameSite=Strict 的临时会话 cookie。
- MCP 使用独立 Bearer token、一次性 grant、短时 attachment ID 与 scope；Agent 不直接接收 SSH 密码。
- 高价值主机首次连接时，仍应通过可信渠道比对指纹。

## 设计参考

[macos.kimi.site](https://macos.kimi.site/) 仅作为桌面隐喻与交互研究参考。NekoSSH 的界面和代码保持原创，不复制第三方脚本、品牌元素或未授权素材。
