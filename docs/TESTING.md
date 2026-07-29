# NekoSSH 测试说明

## 自动验证

```powershell
npm ci
npm run build
npm run web:build
npm test
cargo test --manifest-path src-tauri/Cargo.toml
npm audit
node --check server/ssh2js-server.mjs
```

本 checkout 于 2026-07-29 实测：

- 前端：5 个测试文件、22 项测试通过。
- Node：9 项会话生命周期、真实本地 SSH fixture 与并发终端测试通过。
- Rust：16 项测试通过。
- Tauri 与 Web 两种生产构建均通过，且互不包含对方主界面 chunk。

自动测试应覆盖：

- Tauri 只注册 `ide_*` IPC，不注册 Vault、同步、外部 SSH 或 opener 命令。
- Rust `ssh2` 的 host-key prepare/trust/mismatch、PTY 边界与 SFTP 输入限制。
- Web API 的 Origin、cookie owner、连接 TTL、terminal output 上限与两阶段删除。
- MCP token、版本协商、一次性 grant、attachment 隔离、scope 与硬 TTL。
- 页面刷新/退出清理、活跃连接续期、过期连接不复活、共享 SSH transport 的通道错误隔离。
- Tauri 与 Web 两种 Vite mode 分别构建，不携带另一入口的主界面 chunk。

## 已完成的真实主机验证

2026-07-29 使用用户授权的隔离 SSH 主机完成 Node `ssh2js` 路径验证：

- 首次主机密钥确认与密码认证。
- 持久 PTY 写入、轮询输出。
- SFTP `/tmp` 列表。
- 创建临时目录、上传测试文件、改名、下载并校验内容。
- 两阶段删除测试文件与空目录，远端临时数据已清理。
- MCP grant → attachment → `ssh_exec`。

验证过程没有把密码写入仓库、Vault 或日志。

## 外部浏览器 GUI 回归

2026-07-29 使用本机 Playwright 浏览器验证：

- macOS 桌面、Dock 关闭/重开、窗口拖拽。
- Finder 目录导航、右键菜单、删除确认、文件拖放上传。
- Terminal 输入与输出渲染。
- Agent 窗口与一次性授权生成。
- `5173` 被其他项目占用时，启动脚本自动使用 `5174`；停止后本次监听全部清理。

当次真实主机后来开始主动关闭 `ssh2` 握手；GUI 回归使用网络模拟响应，只验证界面交互，不计为新增远端功能验证。真实 SSH/SFTP/MCP 结果以上一节为准。

## 手工验收

1. `npm run web` 必须打开系统默认浏览器，而不是 Tauri WebView。
2. Web macOS 必须显示桌面、Dock、Terminal、Finder、可拖拽窗口、文件拖放和中文右键菜单。
3. `npm run tauri:dev` 必须显示传统 SSH IDE：连接列表、中文连接过程、左文件区、中终端、右会话状态。
4. 两端首次连接都必须显示指纹；拒绝或关闭时不得继续认证。
5. 断开后终端、传输、Agent attachment 与进程内密码必须失效。
6. 使用错误 Origin、错误 MCP token、错误协议版本和过期 attachment 时必须被拒绝。
