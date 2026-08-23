# Terminal233

Terminal233 是面向 SSH/SFTP/PTY 的桌面客户端与自部署凭据同步服务器，目标是替代 XTerminal、MobaXterm 一类工具。原项目名为 nekossh，现正式改为 Terminal233。

## 目录结构

| 目录 | 作用 |
| --- | --- |
| terminal233/ | Windows 原生客户端。以 [neko233-com/rdesktop](https://github.com/neko233-com/rdesktop) 作为窗口/WebView/IPC 框架，桌面 SSH 由进程内纯 Rust puressh 完成。 |
| terminal233-server/ | Go 自部署同步服务器。用户可以自行部署，注册必须携带官方 token；每个用户最多保存 1000 条 SSH 凭据。 |
| server/ | 独立浏览器 Web 工作台的 Node ssh2 本地后端，不经过 Go 同步服务器转发 SSH。 |

客户端不调用 ssh.exe、Windows OpenSSH、PuTTY、plink 或外部终端。SSH 密码、私钥和主机信任在客户端只存在于进程内；云同步服务器上的密码使用 AES-GCM 加密存储，只有 SSH 实际验证成功后才会写入。

## 本地运行客户端

本机开发要求：Windows x86_64、Node.js 24.18.1、npm 12.0.2、Rust 1.97.1、MSVC Build Tools、Windows SDK 和 WebView2 Runtime。当前本机只验证 Windows x86_64；Windows ARM64 暂不产出，后续只允许在用户明确说“发布”后由 tag-only GitHub Actions 生成。服务端只发布 Linux amd64 与 arm64。

corepack npm ci
corepack npm run build
corepack npm run terminal233:build
.\terminal233\target\release\terminal233.exe --version

生成 Windows 桌面包：

corepack npm run terminal233:package:windows

产物位于 terminal233/target/release/bundle/windows/，包含 Terminal233.exe、dist/、安装/卸载脚本和 ZIP。Windows 静默安装默认使用 C:\App\Terminal233，并注册当前用户 Start Menu 快捷方式及“应用和功能”卸载项：

corepack npm run terminal233:install:windows

也可以从解压后的桌面包目录执行 .\install-terminal233-windows.ps1；卸载使用安装目录中的 .\uninstall-terminal233-windows.ps1。桌面客户端使用系统 WebView2 Runtime，不将 WebView2 安装程序重复打进包内。

客户端默认从 Terminal233.exe 同目录的 dist/ 加载前端；如需指定前端目录，可设置 TERMINAL233_FRONTEND_ROOT。

浏览器调试入口与桌面端共享同一个 Terminal233 SSH IDE：

```powershell
corepack npm run browser:dev
```

它会启动同源 localhost Node `ssh2js` 后端和 `http://127.0.0.1:1420/` 浏览器 IDE；浏览器入口只使用 Web 后端，桌面入口只使用进程内纯 Rust `puressh`，二者共享界面、连接流程、终端与 SFTP 数据约定。

## 发布与自动更新

普通开发流程不使用 GitHub Actions。只有用户明确说“发布”后，才允许创建正式 tag 并触发未来的 tag-only GitHub Actions；当前仓库不新增 workflow。手工发布流程一次完整发布会生成：

- Windows 桌面端：`x86_64`；ARM64 仅保留为未来发布 CI 目标
- Linux 同步服务端：`amd64`、`arm64`

```powershell
corepack npm run terminal233:package:release
```

产物在 `terminal233/target/release/bundle/release/Terminal233-<version>/`，包含四个平台包、`SHA256SUMS.txt`、`RELEASE_NOTES.md` 和 `terminal233-update.json`。确认已配置 GitHub CLI、仓库保护规则和签名材料后，可手工发布：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/publish-terminal233-release.ps1 -ReleaseDirectory .\terminal233\target\release\bundle\release\Terminal233-<version>
```

桌面客户端启动时会自动检查官方 `terminal233-update.json`，发现新版本后显示对应 Windows 架构的下载链接和 SHA-256 前缀。自建更新清单可通过 `VITE_TERMINAL233_UPDATE_MANIFEST_URL` 覆盖默认地址；清单和资产地址必须使用 HTTPS（本机 localhost 调试地址除外）。

## 自部署同步服务器

服务器不提供默认 master key 或官方 token。管理员账号和密码默认是 root/root，首次部署必须立即通过环境变量修改管理员密码。

$env:TERMINAL233_MASTER_KEY = '至少 16 个字符的随机主密钥'
$env:TERMINAL233_OFFICIAL_TOKEN = '官方发放的注册 token'
$env:TERMINAL233_ADMIN_PASSWORD = '修改后的后台密码'
corepack npm run server:go:build
.\terminal233-server\bin\terminal233-server.exe

常用配置：

- TERMINAL233_SERVER_ADDR：监听地址，默认 127.0.0.1:8787
- TERMINAL233_DATA_PATH：加密数据文件，默认 data/terminal233-server.json
- TERMINAL233_ALLOWED_ORIGINS：允许访问客户端的 Origin，多个值用逗号分隔
- TERMINAL233_SSH_TIMEOUT_SECONDS：保存凭据时的 SSH 验证超时，3–120 秒

管理后台是 /admin，API 包括 /api/v1/register、/api/v1/login、/api/v1/accounts。把服务器暴露到公网前，应使用 HTTPS 反向代理，并把官方 token、master key 和管理员密码放在安全的部署密钥系统中。

## 验证

corepack npm run typecheck
corepack npm run test:ui
corepack npm run test:server
corepack npm run server:go:test
cargo check --manifest-path terminal233/Cargo.toml --locked

完整安全边界见 docs/SECURITY.md。XTerminal 仅作为产品定位参考：[xterminal.cn](https://www.xterminal.cn/)。
