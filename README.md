# NekoSSH

NekoSSH 是使用 Go 1.27 与 MyGo 原生界面构建的 SSH 桌面客户端，配套自部署凭据同步服务。客户端无需 WebView 或外部 SSH 客户端。当前 Windows 程序和快捷方式仍使用 `Terminal233` 名称，以兼容现有安装。

- `client/`：MyGo 原生客户端、进程内 SSH、PTY、SFTP 与同步客户端。
- `server/`：Go 凭据同步服务，不转发 SSH 会话或主动连接用户指定的服务器。
- `Taskfile.yml`：构建、测试和本机验证任务。

## 运行与验证

需要 Go 1.27.3 或更高的 1.27 版本和 [Task](https://taskfile.dev/)。MyGo 固定为 v0.7.4，其终端插件的原生库由打包器下载并校验。首次构建需要网络。

```powershell
task test                         # Go 单元测试、SSH/SFTP 测试、HTTPS 同步联调
task test:desktop:windows         # Windows 原生窗口与 SSH 连接，低优先级
task build:client                 # 构建本机 MyGo 原生程序及终端库
task package:windows:local        # 未签名 Windows 安装包
```

Windows 本机验证安装使用 `pwsh -NoProfile -File client/install-local-windows.ps1`。脚本要求 `main` 干净且与远端一致，重新运行测试和打包，静默安装到 `C:\App\nekossh`，核验版本、文件、开始菜单和卸载注册项。Windows 发布产物不带 Authenticode 签名。

客户端启动时检查本项目的 GitHub 正式发布：先验证发布清单的 SHA-256，再核对当前架构安装包的名称、大小和校验值。用户可选择下载位置；安装包校验通过后才会放到目标路径，不覆盖已有文件，也不会自动运行。网络不可用时不影响本地使用。

客户端支持密码或私钥连接，首次连接前需通过可信渠道核对 SHA256 主机指纹；云端取回的连接如指纹变化会阻止连接。连接列表支持搜索、分组、拖拽排序、右键与键盘调整，本机保存连接元数据但不保存密码或私钥口令。SFTP 支持浏览和创建目录、单文件流式上传、下载、拖入上传、取消传输和删除确认；本机路径可通过原生文件选择器填写，传输不会覆盖已有文件，取消须等待当前文件读写返回。云同步目前支持密码认证的 SSH 连接，私钥文件和私钥口令不会上传。登录同步服务后，可在连接表单中获取并核对指纹，然后直接将密码连接同步到页面显示的个人或团队空间，无需先打开终端会话。

## 自部署同步服务

```powershell
task build:server
```

服务默认只监听 `127.0.0.1:8787`，公网使用必须通过 HTTPS 反向代理。配置、凭据保护、备份和威胁边界见 [server/SECURITY.md](server/SECURITY.md)。服务端持有解密密钥；这不是端到端加密。SSH 密码和账户会在服务端加密保存，不应将未完成安全评估的部署用于高价值凭据。

仓库禁用 GitHub Actions，构建与发布采用本机手工流程；未收到明确发布指令时不创建 Release 或发布 tag。

项目仓库：[neko233-com/nekossh](https://github.com/neko233-com/nekossh)。

发布资产可由 `task package:release` 在干净并已推送的 `main` 上生成，包含未签名 Windows x86_64 安装包、Linux amd64/arm64 同步服务、SHA-256 清单与源码提交清单。生成资产不会自行创建 tag 或 GitHub Release。


云同步客户端默认连接 `https://nekossh.neko233.com`。账号可加入多个团队，团队最多 1000 名成员；团队拥有者可创建带有效期、密码和次数限制的邀请，并管理角色与共享 SSH 凭据。管理员可在 MyGo 客户端管理用户、团队和加密备份。同步服务使用加密 SQLite 存储，容量及安全边界见服务端说明。

在部署该默认服务的 Windows 用户账户中，若存在仅供当前用户解锁的本机注册令牌，云同步界面可通过“使用本机部署注册令牌”读取它。令牌不会随安装包发布，切换到其他服务地址时会从表单清除；其他用户需由服务管理员安全提供注册令牌，或使用团队邀请注册。

本机终端不经过 SSH；Windows 使用 PowerShell 7 和可用的 PSReadLine 显示命令语法颜色。终端支持 ANSI 色彩，远程命令行语法高亮由远端 shell 配置决定。
