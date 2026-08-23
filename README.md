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

环境要求：Windows x64、Node.js 24.18.1、npm 12.0.2、Rust 1.97.1、MSVC Build Tools、Windows SDK 和 WebView2 Runtime。

corepack npm ci
corepack npm run build
corepack npm run terminal233:build
.\terminal233\target\release\terminal233.exe --version

生成 Windows 桌面包：

corepack npm run terminal233:package:windows

产物位于 terminal233/target/release/bundle/windows/，包含 Terminal233.exe、dist/、安装脚本和 ZIP。Windows 静默安装默认使用 C:\App\Terminal233：

corepack npm run terminal233:install:windows

也可以从解压后的桌面包目录执行 .\install-terminal233-windows.ps1。桌面客户端使用系统 WebView2 Runtime，不将 WebView2 安装程序重复打进包内。

客户端默认从 Terminal233.exe 同目录的 dist/ 加载前端；如需指定前端目录，可设置 TERMINAL233_FRONTEND_ROOT。

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
