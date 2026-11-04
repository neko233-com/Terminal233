# MyGo 客户端（迁移中）

这里是 Go 1.27.3 + MyGo 原生界面的新客户端，未使用 WebView。目前包含 SSH 主机指纹确认、密码/私钥连接、终端、会话内连接列表、SFTP 文件操作和云同步原型。连接列表仅保存在当前进程内；安装包和完整产品回归仍未完成，不能替代现有客户端。

在仓库根目录执行 `task test` 或 `task build:client`。`task build:client` 通过固定版本 MyGo 打包客户端及 libghostty-vt 动态库；该任务暂不生成安装程序。

Windows 桌面自动化验证：`powershell -NoProfile -File client/test-desktop.ps1 -Quiet`。它构建仅用于测试的自动化版本，在本机测试 SSH 服务上验证原生窗口与连接，不使用外部 SSH 客户端。
