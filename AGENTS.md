# Terminal233 Product Boundaries

## 当前迁移目标（2026-11-04）

- 应用迁移到 Go 1.27 与 MyGo 原生 UI，不使用 WebView；最终代码目录为 `client/` 和 `server/`，用 Taskfile 构建测试。
- `client/` 是进程内 Go SSH/SFTP/PTY 客户端；`server/` 是自部署凭据同步服务，不承载客户端 SSH 会话。客户端与服务分别位于这两个目录，旧 Node、Rust、WebView 和浏览器入口不再作为产品代码。
- 用户明确要求账号密码云同步；必须使用 HTTPS，验证主机密钥，并保护服务端凭据。不得调用外部 SSH 客户端。

## 平台与发布范围

- 桌面端目标规划包含 Windows `x86_64-pc-windows-msvc` 与 `aarch64-pc-windows-msvc`，安装目录默认为 `C:\App\Terminal233`；当前阶段只产出和安装 Windows x86_64，Windows ARM64 桌面包暂停产出。
- 32 位 Windows、`i686`、`i386` 及其他 32 位桌面目标明确不在范围内。产品文档中的“Windows x86”一律指 x86_64，不得新增 32 位构建、安装包、测试或发布资产。
- 服务端只考虑 Linux `amd64` 与 `arm64`；不发布 Windows 服务端。
- 当前本地发布候选和更新清单不得包含 Windows ARM64 桌面资产；ARM64 桌面资产只允许由未来的 GitHub Actions 发布 CI 生成。
- 桌面端自动检查更新只信任项目发布的更新清单和对应校验值；版本比较、平台筛选和安装包命名必须保持 x86_64/arm64 一致。

## 发布与 GitHub Actions 授权（永久项目规则）

- 普通开发、测试、提交和推送不得触发 GitHub Actions。未来新增的 workflow 必须只监听正式发布 tag（`vMAJOR.MINOR.PATCH`），禁止 `push` 到 `main`、`pull_request` 或其他普通分支触发；除非用户明确授权，也不得提供会产生发布资产的手动 workflow 入口。
- 创建或推送正式发布 tag 是受限操作。只有用户明确口头主动说“发布”（或同等明确的发布指令）时，才允许创建/推送发布 tag 并触发 GitHub Actions；“打包”“测试”“提交”“推送”“准备发布”均不等于发布授权。
- 未获得上述明确授权时，禁止执行 `git tag`、`git push origin v*`、GitHub Release 创建或任何会触发发布 workflow 的操作。不得根据上下文自行推断发布授权。
- 当前阶段不创建 `.github/workflows`、不触发 GitHub Actions、不产出 Windows ARM64 桌面包。后续若用户明确说“发布”，才按 tag-only CI 方案生成并验证发布资产。
- 这组规则是本项目的长期协作记忆；任何后续任务都必须先遵守它，再执行构建、提交或发布动作。

## 客户端和安全约束

- 全部界面使用 MyGo 原生 UI 和 Go；SSH 使用进程内 Go 实现，不调用系统 SSH 客户端。
- 连接列表支持搜索、分组、右键与拖拽排序；文件区支持拖拽上传、右键操作和显式删除确认。
- 中文状态与可读错误、键盘可用性、可访问性及减少动态效果设置应在新界面得到支持。
- 云同步服务采用安全默认配置，凭据与会话不写入日志，服务端不能代替客户端盲信 SSH 主机密钥。

## Git 开发规范

- 所有后续开发、修复、提交与推送只在 `main` 分支进行；不得继续使用或新建 `codex/*` 开发分支。
- 保留用户未提交的本地改动；提交时只暂存当前任务明确涉及的文件。
- 每次新增构建、测试、打包或本机运行产物时都必须同步检查并维护 `.gitignore`；禁止把依赖目录、缓存、日志、临时目录、安装包、发布候选、凭据或签名材料提交到 Git。
- 完成可交付改动后必须提交并推送 `main` 上本任务的全部改动；推送前后确认工作树干净且 `HEAD == origin/main`。

## Windows 开发机交付

- 只要 Windows 版本通过本轮验证，就必须使用命令行构建并静默安装最新的 Terminal233 到 `C:\App\Terminal233`，再核验已安装版本；禁止依赖 GUI 或电脑操控完成安装。
- 开发机安装以当前 `main` 的最新已验证提交为准。若缺少可信 Authenticode 证书，可以安装明确标记为本机验证用途的 unsigned 构建，但禁止把它上传为正式 GitHub Release。
- 普通提交禁止触发 GitHub Actions；当前仓库不新增 `.github/workflows`。Windows x86_64 本机验证包走命令行手工流程；正式 GitHub Release 只有在用户明确说“发布”后才允许创建。
- Windows 安装脚本必须同时验证 `Terminal233.exe --version`、Start Menu 快捷方式和当前用户卸载注册项；默认安装目录固定为 `C:\App\Terminal233`。
- 真实 SSH 验证只能使用用户明确提供的测试目标，并通过幂等、固定目录、自动清理的测试脚本完成；不得把密码写入仓库、日志或远端持久目录。
