# Terminal233 Server

这是 Terminal233 的自部署凭据同步服务器，只发布 Linux `amd64` 与 `arm64` 包。

启动前必须配置：

```sh
export TERMINAL233_MASTER_KEY='至少 16 个字符的随机主密钥'
export TERMINAL233_OFFICIAL_TOKEN='官方发放的注册 token'
export TERMINAL233_ADMIN_PASSWORD='修改后的后台密码'
./terminal233-server
```

默认监听 `127.0.0.1:8787`，管理后台为 `/admin`。公网部署请使用 HTTPS 反向代理，并把数据目录限制为服务账号可读写。
