# NekoSSH 手工发布

NekoSSH 禁用 GitHub Actions。Windows 构建、验收、签名和 GitHub Release 发布全部由维护者在锁定环境中手工完成。发行文件上传为 GitHub Release assets，不进入 GitHub Packages Registry、npm 或 NuGet。

## 强制门禁

- 只允许从干净且已推送的 `main` 构建；`HEAD`、`origin/main`、manifest 与 annotated tag 必须指向同一提交。
- Node.js、npm、Rust、Cargo 和 `cargo-audit` 必须与仓库锁定版本一致；Windows 还需要 Visual Studio C++ Build Tools、完整 Perl 与 NASM。
- 正式发布只接受稳定 SemVer、可信 Authenticode 签名和 RFC 3161 时间戳。`-AllowUnsigned` 永远生成 `publishable=false` 的本机验证产物，发布器会拒绝它。
- `release-signing-policy.json` 必须预先提交真实 signer 的精确 Thumbprint、Subject、SPKI SHA-256 和版本窗口。空 allowlist 会按设计 fail-closed。
- 仓库级 GitHub Actions 必须保持禁用。发布器会在创建草稿前启用 GitHub immutable releases；公开后 tag、标题、说明和 assets 均不可修改。
- 稳定版必须有七天内生成的 Windows 验收回执。验收 VM 不持有生产私钥；回执复制回受控签名机后再签名。

NSIS 是面向个人用户的当前用户安装通道，MSI 是面向管理员部署的机器级通道。两者各自支持同格式升级；不支持跨格式直接覆盖，切换前必须先卸载。

## 1. 配置签名策略

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

模块会同时检查 Code Signing EKU、Thumbprint、精确 Subject、SPKI、公钥版本窗口和私钥可用性。新签名要求证书当前有效；带有效时间戳的历史安装包在证书自然过期后仍可用于升级验收。

## 2. 构建签名资产

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
npm run release:windows '--' `
  -TauriSigningConfig C:\secure\nekossh-signing.json `
  -ExpectedSignerThumbprint 0123456789ABCDEF0123456789ABCDEF01234567
```

仅做本机打包验证：

```powershell
npm run release:windows '--' -AllowUnsigned
```

脚本从全新的 Cargo target 构建，执行双入口生产构建、测试、Clippy、依赖审计和第三方许可生成，然后核验 MSI ProductCode/UpgradeCode、签名身份与全部哈希。正式签名集合只包含真正分发的 Tauri 主程序、NSIS、MSI 和 Web 启动器；构建用 `cdylib` 不是安装资产。

输出目录 `output/release/v<版本>-<12 位提交>/` 包含：

- `NekoSSH_<版本>_x64-setup.exe`
- `NekoSSH_<版本>_x64_zh-CN.msi`
- `NekoSSH_Web_<版本>_windows_x64.zip`
- `THIRD_PARTY_NOTICES.txt`
- `SHA256SUMS.txt`
- `RELEASE_NOTES.md`
- `release-manifest.json`

`release-manifest.json` 由严格解析器和 `release-manifest.schema.json` 约束，记录提交、构建时间、工具链、MSI 身份、签名策略、签名证书身份及资产哈希。`output/` 被 Git 忽略，不得提交安装包或私钥。

## 3. 预发验收挑战

在受控签名机、VM 启动前生成一次性 256-bit nonce，并把它保存在发布记录中。不要让验收 VM 自行决定 challenge。

```powershell
$bytes = [byte[]]::new(32)
$rng = [Security.Cryptography.RandomNumberGenerator]::Create()
try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
$validationNonce = -join ($bytes | ForEach-Object { $_.ToString('x2') })
$validationNonce
```

同时记录不可变 VM 镜像或快照 ID，例如 `windows-11-24h2@sha256-...`。该字段和 nonce 是操作员见证的程序化验收绑定，不等同于 TPM 或云平台远程证明。

## 4. 干净 Windows 验收

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

## 5. 在受控机器签名回执

受控签名机必须再次满足干净 `main == origin/main`，并持有策略允许的证书私钥：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/sign-windows-smoke-receipt.ps1 `
  -ReleaseDirectory <发行目录> `
  -SignerThumbprint 0123456789ABCDEF0123456789ABCDEF01234567 `
  -TimestampUrl https://可信时间戳服务地址 `
  -ExpectedValidationNonce $validationNonce `
  -ExpectedCleanVmImageId '<不可变镜像或快照 ID>'
```

签名器会重新验证 manifest、所有文件哈希、两套升级元数据、验证脚本和签名策略哈希，再对回执做时间戳签名和原子替换。

## 6. 发布不可变 GitHub Release

```powershell
$package = Get-Content package.json -Raw | ConvertFrom-Json
$shortCommit = (git rev-parse --short=12 HEAD).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Unable to read HEAD.' }
$releaseDir = (Resolve-Path "output/release/v$($package.version)-$shortCommit").Path

npm run release:publish '--' `
  -ReleaseDirectory $releaseDir `
  -WindowsSmokeReceiptPath (Join-Path $releaseDir 'WINDOWS_SMOKE_RECEIPT.psd1')
```

发布器会：

1. 拒绝 unsigned、prerelease、过期产物、脏工作树、错误仓库或被启用的 Actions。
2. 核对上一稳定版的不可变状态，以及回执中的上一版 NSIS/MSI GitHub digest。
3. 启用 immutable releases，创建或严格续验 annotated tag 与 draft Release。
4. 校验标题、说明、asset 名称/大小/state/GitHub digest，下载全部草稿资产并重新验证签名 Web 自检。
5. 公开稳定版并设为 latest；公开后再次下载、核对哈希，验证 GitHub release attestation 和每个 asset attestation。

不可变 Release 发布后不能替换错误文件；如有问题必须提升版本并创建新 tag。
