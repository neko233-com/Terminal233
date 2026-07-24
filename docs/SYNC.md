# NekoSSH 加密同步说明

NekoSSH 采用“用户自有存储 + 本地端到端加密”：不需要 NekoSSH 账号，远端只保存 `neko233-v1` 的加密 Vault 封套。支持 WebDAV 和 Git（GitHub、Gitea 或兼容 HTTPS Git），但两种协议的冲突与认证边界不同。

## 可执行的同步方式

| 方式 | 可执行配置 | 传输实现 | 冲突语义 |
| --- | --- | --- | --- |
| WebDAV | HTTPS 端点、干净相对路径、Basic 用户名+secret 或 Bearer secret | Rustls HTTPS `PUT`/`GET`；30 秒超时、禁重定向、16 MiB 上限。 | 首次 `If-None-Match: *`，后续 `If-Match: <已保存 ETag>`；409/412 返回冲突且不覆盖。 |
| Git | **HTTPS remote + Vault 内 access token** | 进程内 libgit2；token 只在受限 origin 的凭据回调中使用。 | 拉取当前分支后非强制 push；远端变化/拒绝即失败，不 force-push。 |
| Git SSH | `ssh://…` / `git@host:path` 可被资料格式识别。 | 执行器明确拒绝。 | 不会退回到 SSH Agent 或不受 NekoSSH 主机密钥策略保护的行为。 |

WebDAV 的目标目录必须已经存在。Git 加密工件固定为 `.nekossh/vault.bundle`，提交在用户配置的分支上；其他已有 Git 树内容会保留，但 NekoSSH 不提供仓库内容管理或历史浏览 UI。

## 上传流程

1. 在桌面端创建并解锁 Vault，设置至少 12 个字符的主密码。
2. 配置 WebDAV 或 Git HTTPS token；配置与 secret 加密保存在 Vault 中。
3. 后端从已解锁 Vault 复制**已加密**封套和配置快照后释放 Vault 锁，再执行网络 I/O。解密后的 SSH 资料不会被上传。
4. 成功时返回不含 secret 的回执（协议、操作、密文 SHA-256、字节数）。只有收到此回执，界面才应显示“已备份”。

### WebDAV ETag 条件写入

首次向一个端点/路径上传时，NekoSSH 使用 `If-None-Match: *`，只接受创建新对象；若该位置已存在文件，服务器应返回 412，NekoSSH 将其作为冲突而非覆盖。成功响应必须返回安全 ETag，且该 ETag 与端点、路径一起加密写入 Vault。

之后的上传使用 `If-Match` 携带上次成功的 ETag。服务器返回 409/412、未返回可用 ETag，或上传期间用户改了同步配置时，操作失败且不会认可新的本地版本状态。此时应下载/审阅远端备份，或选择新的空目标路径；不要通过删除版本检查来强制覆盖。

### Git HTTPS token 与非强制 push

NekoSSH 使用 vendored libgit2，不启动 `git.exe`。GitHub 使用 `x-access-token` 用户名，Gitea/通用 Git 使用 `oauth2`；token 从 Vault 仅在内存中的 libgit2 callback 中取用，并且 callback 只匹配已配置的 HTTPS scheme、host 和 port。任何重定向或其他 origin 都得不到 token。

上传会获取已配置分支、在临时 bare repository 中构造只含加密 bundle 的新提交，再进行非强制 push。远端在 fetch 之后出现并发提交或拒绝更新时，push 会失败并保留远端数据；用户需重新下载/审阅后再决定下一步。Git SSH 目前是安全拒绝，不是降级路径。

## 下载、验证、恢复与回滚

下载 API 可以只取得加密 bundle，供显式审阅；更推荐使用桌面端的 WebDAV/Git“安全恢复”命令。该命令在 Rust 内完成，下载包不会经由 WebView：

1. 当前 Vault 必须已解锁，以读取加密保存的同步配置和当前封套快照。
2. Rust 下载远端加密包，并用用户输入的**备份主密码**验证 KDF 参数、包装 DEK、AEAD 负载和资料结构。
3. 验证或下载失败时，当前 Vault 仍保持原样和原解锁状态。
4. 验证成功后，Rust 再检查下载期间本地封套/配置未变；若变化，返回冲突。
5. Rust 清除内存解锁状态，将当前有效 Vault 原子保存为 `neko233-v1.vault.json.bak`，再原子写入远端封套。导入后的 Vault 保持锁定。

如果导入结果不符合预期，使用“恢复导入前备份”命令：保持 Vault 锁定，输入**导入前本地 Vault 的旧主密码**，验证 `.bak` 后原子恢复。失败恢复不会删除唯一的回滚副本。

## 目的地规则

| 目标 | 示例 | 必须满足的规则 |
| --- | --- | --- |
| WebDAV | `https://dav.example.com/root/` + `nekossh/vault.bundle` | 仅 HTTPS；URL 不可携带账号、密码、query 或 fragment；路径必须为干净相对路径，不含 `..`、反斜杠或控制字符。 |
| Git HTTPS | `https://github.com/org/nekossh-vault.git` | 不可嵌入凭据、query 或 fragment；需要 Vault 内 access token；分支名须通过 Git ref 校验。 |
| Git SSH | `git@github.com:org/nekossh-vault.git` | 可保存为资料，但当前上传/下载会返回“不支持”，不能当作备份方案。 |

推荐为 NekoSSH 创建独立的私有仓库或 WebDAV 目录，并赋予最小权限。Git token 应可撤销、仅限必要仓库和最小 scope；不要把 token 写入 `.git/config`、环境变量或 URL。

## 已知限制

- WebDAV 没有自动合并、版本历史或冲突副本 UI；条件写入的目的在于阻止静默最后写入覆盖。
- Git 会产生远端提交，但应用尚无历史浏览、选择版本或合并工作流。
- Git SSH 被主动拒绝，直到其 host-key 验证可与 NekoSSH 的应用专属 known-hosts 策略完全接合。
- 同步不替代主密码管理：远端密文的恢复仍需要相应备份主密码。
- Web/浏览器预览不保存凭据、不执行上传/下载，也不建立 SSH 连接；云同步必须在桌面端运行。
