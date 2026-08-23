# rdesktop 集成说明

本目录只保存 Terminal233 对 `rdesktop` 的集成说明，不包含上游源码副本。实际依赖由 `terminal233/Cargo.toml` 的 Cargo git dependency 管理。

## 当前约束

- 上游项目：`https://github.com/neko233-com/rdesktop`
- Terminal233 必须精确锁定已验证的 git revision；修改 revision 前必须重新验证桌面端构建、纯 Rust SSH 依赖和安装包。
- 桌面端正式目标只有 Windows `x86_64-pc-windows-msvc` 和 `aarch64-pc-windows-msvc`。
- 32 位 Windows、`i686`、`i386` 及其他 32 位目标不支持，也不生成对应的构建、安装包或更新资产。
- Terminal233 Server 与桌面端分离，只发布 Linux `amd64` 和 `arm64` 服务端包。

## 验证与发布

从 Terminal233 仓库根目录执行：

```powershell
corepack npm run verify
corepack npm run terminal233:package:windows
corepack npm run terminal233:package:release
```

ARM64 工具链、Windows linker 或其他必要条件缺失时，构建必须明确失败并修复环境；不得把 x86_64 产物伪装成 ARM64，也不得恢复 32 位目标来绕过问题。

更新清单必须只包含实际生成并校验过的 x86_64/arm64 桌面资产和 Linux amd64/arm64 服务端资产。发布仍采用仓库文档约定的手工流程，不能新增 GitHub Actions。
