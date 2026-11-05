# MyGo 客户端（迁移中）

这里是 Go 1.27.3 + MyGo 原生界面的新客户端，未使用 WebView。目前包含 SSH 主机指纹确认、密码/私钥连接、多会话终端、本机连接列表、SFTP 文件操作和云同步原型。可同时打开最多 12 个本机或 SSH 终端，每个会话保留独立的远程文件位置。本机连接列表保存地址、收藏和最近成功连接时间等元数据，不保存 SSH 密码或私钥口令；用户应通过可信渠道核对主机指纹并验证功能。

在仓库根目录执行 `task test` 或 `task build:client`。`task build:client` 通过固定版本 MyGo 打包客户端及 libghostty-vt 动态库；该任务暂不生成安装程序。

Windows 桌面自动化验证：`pwsh -NoProfile -File client/test-desktop.ps1 -Quiet`。它构建仅用于测试的自动化版本，在本机测试 SSH 服务上验证原生窗口与连接，不使用外部 SSH 客户端。

Windows 安装包：`task package:windows:local`。`pwsh -NoProfile -File client/install-local-windows.ps1` 要求干净且与远端一致的 `main`，执行测试并重新打包，再静默安装至 `C:\App\nekossh`，核验版本、文件哈希、开始菜单与卸载注册项；Windows 构建不带 Authenticode 签名。


本机终端使用 PowerShell 7（若安装了 PSReadLine 则加载命令语法高亮）；远程 shell 的高亮由远端配置决定。团队邀请可通过链接和密码注册或加入，邀请可撤销；平台管理员通过动态验证码执行用户、团队和加密备份管理。默认云地址为 `https://nekossh.neko233.com`，部署尚需完成。
