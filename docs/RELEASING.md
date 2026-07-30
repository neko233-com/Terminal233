# NekoSSH 手工发布

NekoSSH 禁用 GitHub Actions。Windows 构建、验收、签名和 GitHub Release 发布全部由维护者在锁定环境中手工完成。发行文件上传为 GitHub Release assets，不进入 GitHub Packages Registry、npm 或 NuGet。

## 强制门禁

- 只允许从干净且已推送的 `main` 构建；`HEAD`、`origin/main`、manifest 与 annotated tag 必须指向同一提交。所有 tracked 文件的 index 与工作树必须逐字节对应 `HEAD`，任何 `skip-worktree`、`assume-unchanged`、未合并状态、大小写路径冲突、symlink 或 submodule 都会 fail-closed。
- Node.js、npm、Rust、Cargo 和 `cargo-audit` 必须与仓库锁定版本一致；Windows 还需要 Visual Studio C++ Build Tools、完整 Perl 与 NASM。
- 正式打包和发布入口只能直接调用 `powershell.exe -File ...`，不能先经过 npm。运行任何 Git、GitHub CLI、Node.js 或 npm 命令前，脚本都必须以命令行单独提供的 SHA-256 锚定仓库外的 builder toolchain lock。
- 正式发布只接受稳定 SemVer、可信 Authenticode 签名和 RFC 3161 时间戳。`-AllowUnsigned` 永远生成 `publishable=false` 的本机验证产物，发布器会拒绝它。
- `release-signing-policy.json` 必须预先提交真实 signer 的精确 Thumbprint、Subject、SPKI SHA-256 和版本窗口。空 allowlist 会按设计 fail-closed。
- 仓库级 GitHub Actions 必须保持禁用。发布器会在创建草稿前启用 GitHub immutable releases；公开后 tag、标题、说明和 assets 均不可修改。
- 稳定版必须有七天内生成的 Windows 验收回执。验收 VM 不持有生产私钥；回执复制回受控签名机后再签名。

NSIS 是面向个人用户的当前用户安装通道，MSI 是面向管理员部署的机器级通道。两者各自支持同格式升级；不支持跨格式直接覆盖，切换前必须先卸载。

## 1. 固定外部 builder 工具链

在受控、已完成软件来源核验的 Windows x64 builder 上生成一次机器专用 lock。实际 lock 必须放在仓库外的受保护本地固定卷；仓库中的 `release-toolchain-lock.example.json` 只解释格式，故意不可用于发布。

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/new-release-toolchain-lock.ps1 `
  -OutputPath C:\secure\nekossh-builder-toolchain.json `
  -NpmCliPath C:\trusted\npm\12.0.2\node_modules\npm\bin\npm-cli.js `
  -NodePath C:\trusted\node-v24.18.1-win-x64\node.exe `
  -RustcPath (rustup which --toolchain 1.97.1-x86_64-pc-windows-msvc rustc) `
  -CargoPath (rustup which --toolchain 1.97.1-x86_64-pc-windows-msvc cargo) `
  -PerlPath C:\trusted\strawberry-perl\perl\bin\perl.exe `
  -NasmPath C:\trusted\strawberry-perl\c\bin\nasm.exe `
  -SignToolPath 'C:\Program Files (x86)\Windows Kits\10\bin\<SDK 版本>\x64\signtool.exe'
```

`RustcPath` 与 `CargoPath` 必须是 `rustup which --toolchain <锁定工具链> ...` 返回的实际 toolchain binary，不能是 `%USERPROFILE%\.cargo\bin\` 下的 rustup proxy；门禁还会拒绝字节完全相同的 rustc/Cargo proxy。生成器输出 `expectedSha256`。把这 64 位小写摘要保存到与 lock 分离的发布记录或离线介质；打包、回执签名与发布时必须通过 `-ExpectedToolchainLockSha256` 显式提供，禁止从 lock 同目录的辅助文件、环境变量或仓库配置自动读取。

lock 使用严格 JSON，拒绝重复/大小写歧义/未知字段。它固定 PowerShell、Git、GitHub CLI、Node.js、npm CLI、rustc、Cargo、cargo-audit、Perl、NASM、vswhere 和 SignTool 的唯一规范绝对路径、文件长度、SHA-256、精确版本，以及 Authenticode 状态。每条路径必须是本地固定卷上的普通文件且整条路径不得经过 reparse point。已签名工具精确绑定 Subject、Thumbprint、SPKI SHA-256 和是否带时间戳；未签名工具必须明确记录 `NotSigned`、空 signer 字段和文件哈希。发布器会把 lock 原始文件哈希及去路径的 observed identities 与 manifest 重验，任何文件或路径替换都会 fail-closed。

`release-toolchain-lock.schema.json` 是交换格式约束。生成 lock 只是 enrollment，不替代维护者对下载来源、安装介质和 builder ACL 的独立核验。

> 当前为工具门禁第一阶段，正式发布保持 fail-closed。只有 `-AllowUnsigned` 本机验证可继续。解除门禁不只是再登记几个 `.exe`：独立锚定的 builder 定义还必须覆盖 rustdoc/Rust sysroot、实际 Tauri CLI/native binding、MSVC/Windows SDK/NSIS/WiX 可执行文件及编译链接输入树、npm/Cargo registry/cache/config、Git tag 验签器与 trust store、非交互凭据路径、干净 VM 验证器，并关闭“验完后替换工具”的 TOCTOU。当前 publisher 中相对寻址的 GPG/SSH verifier、`gh` 的授权/代理环境以及签名阶段 Git 网络路径都因此仍被正式 NO-GO 覆盖；不得删除门禁或增加绕过参数来发布。

## 2. 配置签名策略

先在受控机器读取真实代码签名证书信息，再把它加入 `release-signing-policy.json` 并提交、推送。不得填入示例、自签名或临时证书。

策略条目格式：

```json
{
  "thumbprint": "40 位大写 SHA-1 证书指纹",
  "subject": "证书的精确 Subject",
  "spkiSha256": "64 位小写 SPKI SHA-256",
  "validFromVersion": "0.2.0",
  "validThroughVersion": "0.2.99"
}
```

模块会同时检查 Code Signing EKU、Thumbprint、精确 Subject、SPKI、公钥版本窗口和私钥可用性。新签名还要求 Windows 能构建受信任的代码签名证书链，并在线完成除根证书外的吊销检查；带有效时间戳的历史安装包在证书自然过期后仍可用于升级验收。

## 3. 构建签名资产

Tauri 签名配置必须位于仓库外或忽略目录，只允许以下字段：

```json
{
  "bundle": {
    "windows": {
      "certificateThumbprint": "0123456789ABCDEF0123456789ABCDEF01234567",
      "digestAlgorithm": "sha256",
      "timestampUrl": "https://可信时间戳服务地址",
      "tsp": true
    }
  }
}
```

正式构建：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-windows-release.ps1 `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256> `
  -TauriSigningConfig C:\secure\nekossh-signing.json `
  -ExpectedSignerThumbprint 0123456789ABCDEF0123456789ABCDEF01234567
```

仅做本机打包验证：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/package-windows-release.ps1 `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256> `
  -AllowUnsigned
```

脚本先拒绝隐藏 index 标志并用 `git hash-object --no-filters`、文件长度和原始字节 SHA-256 逐项比对 `HEAD` blob，避免 Git stat 缓存、换行转换或 clean filter 掩盖修改；随后用 `git cat-file blob` 的二进制输出把每个已提交 blob 直接导出到随机的 `output/.source-snapshot-<pid>-<guid>/`，将全部 tracked 文件设为只读，并只从该快照执行构建。快照不含 `.git`，不会运行 smudge filter，也不会覆盖现有 `output/release/` 候选。构建中途和生成 manifest 前都会重新验证原仓库与只读快照；任一 branch、commit、origin、tree、文件内容或保护属性变化都会终止。临时目录只按已解析的任务专属绝对路径清理，清理失败则保留现场并报错。

脚本从全新的 Cargo target 构建，执行源码门禁安全回归、双入口生产构建、测试、Clippy、依赖审计和第三方许可生成，然后核验 MSI ProductCode/UpgradeCode、签名身份与全部哈希。正式签名集合只包含真正分发的 Tauri 主程序、NSIS、MSI 和 Web 启动器；构建用 `cdylib` 不是安装资产。

输出目录 `output/release/v<版本>-<12 位提交>/` 包含：

- `NekoSSH_<版本>_x64-setup.exe`
- `NekoSSH_<版本>_x64_zh-CN.msi`
- `NekoSSH_Web_<版本>_windows_x64.zip`
- `THIRD_PARTY_NOTICES.txt`
- `SHA256SUMS.txt`
- `RELEASE_NOTES.md`
- `release-manifest.json`

`release-manifest.json` 使用 schema v4，由严格解析器和 `release-manifest.schema.json` 约束。除提交、构建时间、工具链、MSI 身份、签名策略、签名证书身份及资产哈希外，`source` 还记录 `main`、完整 commit、Git root tree、tracked 文件数，以及按 ordinal 路径顺序对每项 `mode + UTF-8 路径长度/路径 + 原始 blob 长度/内容` 进行域分离流式 SHA-256 得到的源码摘要（`digestFormat=git-raw-blobs-sha256-v1`）。它不以 Git SHA-1 对象号作为内容摘要；工作树和导出快照还会逐文件复核原始字节长度与 SHA-256。`toolchain` 绑定外部 lock 的原始 SHA-256，以及 12 项工具去除本机路径后的 length/hash/version/Authenticode observed identities。签名、验收和发布阶段都会把这些字段与当前完整 Git 源快照重新绑定，发布阶段还会重新读取外部 lock 并重新观察每个工具。`output/` 被 Git 忽略，不得提交安装包、builder lock 或私钥。

## 4. 预发验收挑战

在受控签名机、VM 启动前生成一次性 256-bit nonce，并把它保存在发布记录中。不要让验收 VM 自行决定 challenge。

```powershell
$bytes = [byte[]]::new(32)
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
$validationNonce = -join ($bytes | ForEach-Object { $_.ToString('x2') })
$validationNonce
```

同时记录不可变 VM 镜像或快照 ID，例如 `windows-11-24h2@sha256-...`。该字段和 nonce 是操作员见证的程序化验收绑定，不等同于 TPM 或云平台远程证明。

## 5. 干净 Windows 验收

在一次性干净 Windows x64 VM 中检出 manifest 对应提交，并保持 `main == origin/main`。把完整发行目录放回相同的 `output/release/...` 路径。VM 需要管理员权限和 GitHub CLI 登录，但不需要生产签名私钥。

首次稳定版：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-windows-release-installers.ps1 `
  -ReleaseDirectory <发行目录> `
  -ExpectedSignerThumbprint 0123456789ABCDEF0123456789ABCDEF01234567 `
  -ConfirmCleanWindowsVm `
  -CleanVmImageId 'windows-11-24h2@immutable-snapshot-id' `
  -ValidationNonce $validationNonce `
  -FirstRelease
```

后续稳定版必须同时使用 GitHub 上一个不可变稳定版的 NSIS 与 MSI：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/test-windows-release-installers.ps1 `
  -ReleaseDirectory <发行目录> `
  -ExpectedSignerThumbprint <当前 signer> `
  -ConfirmCleanWindowsVm `
  -CleanVmImageId '<不可变镜像或快照 ID>' `
  -ValidationNonce $validationNonce `
  -PreviousNsisPath <上一稳定版 NSIS> `
  -PreviousMsiPath <上一稳定版 MSI> `
  -PreviousSignerThumbprint <上一稳定版 signer>
```

验收覆盖 NSIS 和 MSI 的安装、同格式升级、重装、降级阻止、ProductVersion、ARP 注册/卸载命令、全部实际安装 PE 的签名、原生 libssh2 自检、卸载残留，以及签名 Web 可携包的 payload、Node、同源 API 与会话自检。正常安装、升级和卸载不接受需要重启的退出码。

成功后 VM 生成未签名的 `WINDOWS_SMOKE_RECEIPT.psd1`。把未改动的回执复制回原发行目录。

## 6. 在受控机器签名回执

受控签名机必须再次满足干净 `main == origin/main`，并持有策略允许的证书私钥：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/sign-windows-smoke-receipt.ps1 `
  -ReleaseDirectory <发行目录> `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256> `
  -SignerThumbprint 0123456789ABCDEF0123456789ABCDEF01234567 `
  -TimestampUrl http://可信服务的-Authenticode-时间戳端点 `
  -ExpectedValidationNonce $validationNonce `
  -ExpectedCleanVmImageId '<不可变镜像或快照 ID>'
```

PowerShell 数据文件只能通过 `Set-AuthenticodeSignature` 使用传统 Authenticode 时间戳 API；Windows API 不支持 HTTPS 时间戳端点，因此回执参数明确只接受受信服务提供的 HTTP Authenticode 端点，并在签后要求有效 `TimeStamperCertificate`。HTTP 仅承载由 TSA 签名且绑定该回执摘要的响应；分发的 EXE/MSI 仍严格使用 SignTool `/tr ... /td SHA256 /fd SHA256` 的 RFC 3161 时间戳，二者不得混用或虚假标注。

签名器只调用本地、受信任且带时间戳 Authenticode 签名的 `git.exe`，通过共享源码完整性门禁复核全部 tracked 文件、Git root tree 和 source snapshot SHA-256，并逐个确认关键策略、schema 与验证脚本的工作树、索引和 `HEAD` blob 完全一致，再验证 manifest、所有文件哈希、两套升级元数据和回执绑定。私钥操作只在当前签名身份专用、禁用 ACL 继承且限制为当前身份、SYSTEM 与 Administrators 的本地目录中进行；发行目录只接收已经签名并复验的副本。原子替换前保留独立的受保护未签回执，失败时即使目标文件已丢失也会恢复；无法安全恢复时不会删除该受保护备份。

## 7. 发布不可变 GitHub Release

发布器绝不创建 tag。必须先在受控签名仪式中为 manifest 的精确提交创建 cryptographically signed annotated tag，并配置 Git 的 OpenPGP trust policy，或为 SSH tag 签名配置受保护的 `gpg.ssh.allowedSignersFile`。在发布前用锁定的 Git 独立执行 `git verify-tag --raw v<版本>`，记录 OpenPGP 完整 fingerprint 或 `SHA256:` SSH key fingerprint；未签名、签名无效、身份不匹配的 tag 都不得推送。

```powershell
$releaseDir = (Resolve-Path 'output\release\v<版本>-<12位提交>').Path
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/publish-github-release.ps1 `
  -ReleaseDirectory $releaseDir `
  -ToolchainLockPath C:\secure\nekossh-builder-toolchain.json `
  -ExpectedToolchainLockSha256 <离线记录的-64-位小写-SHA256> `
  -ExpectedTagSignerFingerprint <OpenPGP-完整指纹或-SHA256:SSH指纹> `
  -WindowsSmokeReceiptPath (Join-Path $releaseDir 'WINDOWS_SMOKE_RECEIPT.psd1')
```

发布器会：

1. 拒绝 unsigned、prerelease、过期产物、脏工作树、错误仓库或被启用的 Actions。
2. 核对上一稳定版的不可变状态，以及回执中的上一版 NSIS/MSI GitHub digest。
3. 验证已存在的 cryptographically signed annotated tag、精确 signer fingerprint 与远端 peeled commit；只创建或严格续验 draft Release。
4. 校验标题、说明、asset 名称/大小/state/GitHub digest，下载全部草稿资产并重新验证签名 Web 自检。
5. 公开稳定版并设为 latest；公开后再次下载、核对哈希，验证 GitHub release attestation 和每个 asset attestation。

不可变 Release 发布后不能替换错误文件；如有问题必须提升版本并创建新 tag。
