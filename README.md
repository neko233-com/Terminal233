# NekoSSH

NekoSSH 是本地优先的 Rust + Tauri v2 SSH 工作台。它将传统 SSH、原创 macOS 风格桌面预览和安全浏览器预览置于同一界面；SSH 资料、同步凭据和远端版本状态都保存在本地加密保险箱中。

> 当前为 0.1 基础设施版本。桌面、终端和浏览器预览会明确标记为“未连接”，不会伪造远端命令输出、远程桌面状态或网页登录结果。真实操作只在桌面端经受限的 Rust 后端启动。

## 已验证的能力与边界

| 能力 | 当前实现 | 关键边界 |
| --- | --- | --- |
| 本地 Vault | 随机 256-bit DEK、Argon2id、XChaCha20-Poly1305；支持锁定、解锁、资料 CRUD、改主密码、验证导入和回滚。 | 无默认主密码或恢复后门；遗忘主密码不能解密备份。 |
| 传统 SSH | 生成无 shell 的 OpenSSH 参数；Windows 在受信任 Windows Terminal 中启动外部 SSH。密码、内嵌 PEM、WebDAV 密码和 Git token 不进入 argv。 | 没有内嵌 PTY。保险箱内 PEM 私钥不会落盘，须等待受维护的内置 SSH/PTY 适配器。 |
| 主机密钥 | 外部 OpenSSH 使用应用专属 `neko233-known_hosts`、`StrictHostKeyChecking=accept-new`、`GlobalKnownHostsFile=none` 和 `UpdateHostKeys=no`。 | 首次连接采用 TOFU；高价值主机仍应人工比对指纹。变化的主机密钥会被拒绝，而非静默接受。 |
| WebDAV 同步 | Rustls HTTPS 的真实 PUT/GET，首次 `If-None-Match: *` 创建，后续以已保存 ETag 的 `If-Match` 条件写入。 | 409/412 被视为冲突，远端和本地状态不会被静默覆盖；没有自动合并或历史浏览 UI。 |
| Git 同步 | 使用进程内 libgit2 真实上传/下载加密包；仅支持 HTTPS + Vault 内 token，凭据回调严格绑定配置的 HTTPS origin，并使用非强制 push。 | Git SSH / SSH Agent 路径被明确拒绝，直到能复用 NekoSSH 主机密钥策略；非强制 push 被远端拒绝时不会覆盖远端。 |
| 安全恢复 | 云端下载、密码/AEAD 验证、锁定、`.bak` 回滚快照和原子替换均在 Rust 内完成。 | 替换前要求备份密码；恢复后 Vault 保持锁定，且必须由用户显式解锁。 |
| 浏览器工作区 | 不嵌入不可信网页；仅可显式交给系统浏览器打开 HTTPS 或 loopback HTTP 地址。 | 浏览器预览没有 SSH 网络权限，不采集 Vault/云凭据，也不代表已访问或登录。 |

## 安全模型

- `neko233-v1` 是公开的保险箱格式标识，**不是**默认密钥、密码或种子。
- 每个保险箱生成随机数据加密密钥（DEK）。主密码使用 Argon2id v1.3 派生 KEK，仅用于包装 DEK；资料以 XChaCha20-Poly1305 加密并认证。
- 默认 KDF 为 64 MiB、3 次迭代、并行度 1；导入的元数据会被限制在 32–128 MiB、最多 6 次迭代和并行度 4，避免恶意备份触发失控分配。
- 主密码最少 12 个字符。修改密码只重新包装 DEK，资料密文不需要重写。
- 同步 secret 不能出现在 URL、Git remote、进程参数、环境变量、计划、回执或日志中。
- 导入前会验证备份密码、DEK 包装和 AEAD 负载；验证失败不会触碰当前 Vault。

完整的威胁边界、Windows SSH 启动路径与发布硬化项见 [安全说明](docs/SECURITY.md)。

## WebDAV / Git 自有云备份

NekoSSH 不要求 NekoSSH 账号，目标是让用户使用已有的免费或自托管存储：

- **WebDAV**：仅 HTTPS；适用于 NAS、Nextcloud、坚果云和兼容服务。目标目录需预先存在。首次上传只允许创建，随后写入必须匹配上次成功上传保存的 ETag。
- **Git**：GitHub、Gitea 或兼容 Git 服务的 **HTTPS 私有仓库 + 最小范围 token**。libgit2 在进程内处理认证，远端只获得 `.nekossh/vault.bundle` 中的密文。
- **Git SSH 不可用**：即使资料格式可表达 SSH remote，执行器会安全拒绝它，绝不退回到未验证的 SSH Agent/主机密钥行为。
- **恢复**：可通过 WebDAV 或 Git 的 Rust 恢复命令执行“下载 → 验证 → 锁定 → `.bak` → 原子导入”；旧本地 Vault 可用旧密码显式回滚。

同步协议、冲突语义、恢复流程和当前限制详见 [同步说明](docs/SYNC.md)。

## 开发与验证

先安装 Node.js、Rust（本仓库声明 Rust 1.96）以及 Tauri v2 对应平台依赖。

```powershell
# 复现锁定的前端依赖，并启动仅绑定本机回环地址的 Vite 开发服务器
npm ci
npm run dev

# 前端类型检查、生产构建与单元测试
npm run build
npm test

# Rust 核心、Vault、同步与 Tauri 配置测试
cargo test --manifest-path src-tauri/Cargo.toml
```

如已安装 Tauri CLI，可用 `cargo tauri dev` 启动桌面壳。当前 checkout 的可复现实测计数、测试边界和手工验收项见 [测试说明](docs/TESTING.md)。

## 设计参考与素材来源

[macos.kimi.site](https://macos.kimi.site/) 用作桌面隐喻与交互研究参考；XTerminal 用作功能研究参考。NekoSSH 的界面、图标和代码须保持原创。

本仓库**不会**自动下载、打包或复制上述网站的脚本、Apple 标识、壁纸、品牌元素或其他未明确授权的素材。任何日后加入的第三方资源都必须先确认许可证或取得书面授权，并记录来源、版本、许可证、校验和与落盘位置。规则和清单模板见 [参考与素材说明](docs/REFERENCES.md)。

## 仓库卫生

请勿提交 SSH 私钥、WebDAV 密码、Git token、加密 Vault、回滚备份或应用专属主机密钥文件。根目录 [`.gitignore`](.gitignore) 已覆盖这些文件及构建产物；新增凭据格式、备份格式或本地数据目录时，请同时更新它。
