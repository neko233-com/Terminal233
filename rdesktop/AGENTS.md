# rdesktop 集成约束

- 这是 Terminal233 的集成文档目录，不是 Cargo git checkout；不要把 Cargo 缓存目录当作提交目标，也不要直接在缓存中维护长期修改。
- `terminal233/Cargo.toml` 中的 `rdesktop` git revision 是依赖边界。需要上游修复时，先在上游完成变更，再更新 revision 并重新验证。
- 当前锁定 revision 为 `e9ad03a14f6519a730389a00ba25f2337ba6f3be`；Terminal233 打包默认使用无系统装饰的一体化窗口，系统窗口 UI 通过 `window.__RDESKTOP_WINDOW__.setDecorations(true|false)` 实时切换。
- 浏览器开发模式必须继续保持默认入口，原生桌面与浏览器只能替换渲染/SSH 适配层，不能分叉 UI 交互契约。
- 只支持 Windows `x86_64-pc-windows-msvc` 与 `aarch64-pc-windows-msvc` 桌面端。32 位 Windows、`i686`、`i386` 和其他 32 位目标明确排除，禁止新增相关 target、脚本、测试或发布资产。
- 服务端只支持 Linux `amd64` 与 `arm64`，服务端发布不能混入桌面端安装包。
- 不得引入 Windows OpenSSH、`ssh.exe`、PuTTY、plink、Windows Terminal 或其他外部 SSH 客户端；桌面端 SSH、PTY 和 SFTP 必须保持纯 Rust 进程内实现。
- 不得引入 `ssh2` / libssh2、OpenSSL / `openssl-sys`、`ring`、`aws-lc-*` 或原生 C/ASM SSH/密码学实现。
- 构建失败时报告真实的 target、linker 和依赖问题；不通过改名、复制或伪造产物来“通过”架构验证。
