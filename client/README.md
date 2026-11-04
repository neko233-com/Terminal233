# MyGo 客户端（迁移中）

这里是 Go 1.27.3 + MyGo 原生界面的新客户端，未使用 WebView。当前只实现 SSH 主机指纹确认、密码/私钥连接和终端原型；SFTP 文件操作与云同步均为原型；完整连接列表、拖拽、右键与安装包尚未迁移，也仍需完整端到端验证，不能替代现有客户端。

在仓库根目录执行 `task test` 或 `task build:client`。终端组件依赖 MyGo 的 libghostty-vt 动态库，裸 `go build` 仅验证可编译；完整分发需使用 MyGo 打包流程并验证依赖随包提供。
