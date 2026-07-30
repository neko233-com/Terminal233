# NekoSSH 测试说明

## 自动验证

核心稳定性与安全边界可完全通过 CLI 验证；本流程不需要浏览器自动化、桌面操控或截图。

```powershell
npm ci
node --check server/ssh2js-server.mjs
npm run verify
npm run audit:deps

# 不可发布的本机打包验证
npm run release:windows '--' -AllowUnsigned
```

仓库禁用 GitHub Actions。手工发布环境固定使用 Node.js 24.18.1、npm 12.0.2 与 Rust 1.97.1（`.nvmrc`、`packageManager`、`rust-toolchain.toml`）。
Windows 上运行 Rust 验证前，还必须确保 Visual Studio C++ Build Tools、完整 Perl（`perl -MLocale::Maketext::Simple` 可成功执行）和 NASM 可用；它们用于从锁定依赖构建 vendored OpenSSL，发布脚本会在编译前显式检查。

`npm run verify` 会执行 Tauri renderer 与 Web macOS renderer 的生产构建、运行时包隔离检查、前端与 Node 测试，以及 Rust fmt、Clippy 和测试。测试数量随覆盖面增长，不在文档中写死。

`npm run release:windows` 从全新的 Cargo target 构建并精确收集当前版本 NSIS/MSI，同时生成独立 Web macOS Windows 便携 ZIP、第三方许可、`SHA256SUMS.txt` 与提交绑定 manifest。正式构建必须传入本机 Tauri 签名配置和策略允许的证书指纹，并要求真正分发的 Tauri 主程序、NSIS、MSI 与 Web 启动器均为有效时间戳签名；固定 `node.exe` 必须匹配官方哈希和 OpenJS 签名。`-AllowUnsigned` 仅允许本机静态打包验证，manifest 固定为 `publishable=false`，且验证器不会执行未签名 EXE。详见 [手工发布说明](RELEASING.md)。

稳定版还要求受控机器先生成一次性 nonce，再在一次性干净 Windows x64 VM 中以管理员身份执行 `scripts/test-windows-release-installers.ps1`。验收覆盖 NSIS 与 MSI 的安装、同格式升级、重装、降级阻止、ProductVersion、ARP 注册/卸载命令、卸载残留，全部实际安装 PE 与卸载器签名，Tauri 原生 libssh2 自检，以及签名 Web 便携包的 payload/Node/同源 API 自检。VM 只生成未签回执；受控签名机在七天内复验 nonce、镜像 ID、脚本/策略及全部资产哈希后再签名。

自动测试应覆盖：

- Tauri `AppManifest`、精确 `allow-ide-*` capability、生产/开发 CSP 分离，以及无 opener、shell、外部进程权限。
- Rust `ssh2` 的 host-key prepare/trust/mismatch、DSA 拒绝、PTY 边界与 SFTP 输入限制。
- Tauri PTY 原始字节 Channel、逐块 ACK、ACK 超时、输入背压与断开清理。
- Tauri SFTP 的上传边界、下载 started/chunk/completed 顺序、逐块 ACK、任务取消与大小/并发上限。
- Tauri 阻塞 SSH 操作的全局/单连接准入上限、断开屏障、有界 drain、空闲过期回收与迟到资源自清理。
- Web API 的 Origin、cookie owner、连接 TTL、原始 PTY 字节、terminal output 上限、输入背压与两阶段删除。
- MCP token、版本协商、一次性 grant、attachment 隔离、scope 与硬 TTL。
- 页面刷新/退出清理、活跃连接续期、过期连接不复活、共享 SSH transport 的通道错误隔离。
- Tauri 与 Web 两种 Vite mode 分别构建，不携带另一入口的主界面 chunk。
- Web 便携启动器拒绝 payload 哈希、额外文件、路径、官方 Node 哈希或签名异常，并在 Node 权限模式下完成无浏览器同源自检。

## 隔离 SSHD 验收

需要验证真实协议互操作时，只使用授权的隔离 SSHD，并在测试结束后清理远端数据：

1. 分别从 Web 与 Tauri 完成首次指纹确认、密码认证和错误密码回退；不要复用两端的连接或凭据状态。
2. 运行持续输出、Unicode、全屏 TUI 与 PTY resize 场景，确认断流、背压或关闭都能得到明确反馈。
3. 覆盖 SFTP 列表、上传、下载、改名、空目录删除、显式删除确认、大小上限、取消和网络中断。
4. Web 额外验证 MCP grant → attachment → `ssh_exec`，以及错误 Origin、token、协议版本和过期 attachment 的拒绝路径。
5. 断开后确认终端、传输、attachment、待确认主机密钥与进程内凭据全部失效。

不得把生产密码、私钥或主机数据写入仓库、日志、测试快照或持久测试夹具。
