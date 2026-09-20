# Meshlink 发布流水线运行手册

本文档对应 Task 11.3（11C）。流水线只固定测试、构建、签名门禁、打包、更新清单、校验和、发布说明、重复打包校验和回滚产物，不绑定 CI 厂商、制品库、在线签名服务或部署平台。

## 模式与安全边界

| 模式 | 用途 | 签名行为 | 失败条件 |
| --- | --- | --- | --- |
| `Development` | 本地开发、QA、可重复流程验证 | 清单和包内元数据明确写入 `code_signed=false`，release notes 标记 unsigned | 版本、文件名、内部版本、哈希、必需文件、秘密扫描、包内清单或重复打包不一致时失败 |
| `Release` | 正式发布候选 | 五个 Windows EXE 必须由本机 `CurrentUser\My` 证书存储中的 Authenticode 代码签名证书签名 | 缺证书指纹、证书不存在/无私钥/用途不符、签名无效、签名者不一致、缺上一稳定包时立即失败 |

仓库不得存放私钥、PFX/P12、证书密码、访问令牌或在线签名凭据。正式模式只接收证书指纹；证书私钥由 Windows 证书存储及组织既有权限控制。`MESHLINK_SIGNING_CERT_THUMBPRINT` 只是公开证书标识，不是秘密。

## 从干净 checkout 执行

前提：机器已具备仓库当前使用的 Go、Windows PowerShell 和 Windows 构建环境。流水线不会安装依赖，也不会联网下载工具。

先执行专项测试：

```powershell
go test -count=1 ./internal/update
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-release-pipeline.ps1
cmd /d /c call publish-update.bat -Help
```

开发模式的一条命令完整流程：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release.ps1 -Mode Development
```

该命令固定执行一次全量 `go test -count=1 ./...`，构建 Windows/Linux 产物，生成 unsigned 开发包，再用同一批构建输入进行第二次确定性打包并逐字节比较 ZIP。也可分步排查：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Mode Development
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\package.ps1 -Mode Development
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-release.ps1 -ReleaseDir .\release -Version (Get-Content -Raw .\VERSION).Trim() -ExpectedMode Development
```

`build.ps1 -Package` 仍保留原标准包行为，`build.ps1 -PrivatePackage` 仍调用既有企业私有化打包流程；免费、自建、官方 Hub、团队和企业功能不因 11C 改变。

## 正式发布

正式发布前，先在组织受控流程中把代码签名证书配置到执行账号的 `CurrentUser\My` 证书存储，并准备上一稳定版本的已校验 ZIP。不要把证书文件或密码放进仓库或命令历史。

```powershell
$env:MESHLINK_SIGNING_CERT_THUMBPRINT = "40_HEX_CERTIFICATE_THUMBPRINT"
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release.ps1 `
  -Mode Release `
  -PreviousPackagePath C:\approved-artifacts\meshlink-0.1.4.zip
Remove-Item Env:\MESHLINK_SIGNING_CERT_THUMBPRINT
```

上一稳定包文件名必须是 `meshlink-<previous_version>.zip`，且不得与当前 `VERSION` 相同。正式模式缺少证书配置或回滚包会在测试/构建前清晰失败，不会降级成 unsigned 发布。

发布到现有内网更新服务可继续使用：

```bat
publish-update.bat 0.1.5 10.77.0.1:1263 C:\meshlink-release Release C:\approved-artifacts\meshlink-0.1.4.zip
```

第一个版本参数只用于一致性校验，不再写入 `VERSION`。脚本在发布校验成功后才安装并重启 `MeshlinkUpdateServer`。

## 产物与审计

默认输出：

- `bin/build-metadata.json`：唯一版本、固定构建时间、模式、签名标记和构建产物 SHA-256。
- `dist/meshlink/` 与 `dist/meshlink.zip`：兼容既有本地分发目录和文件名。
- `release/meshlink-<version>.zip`：更新包，根目录固定为 `meshlink/`。
- `release/manifest.json`：更新服务清单，固定 schema、版本、模式、包文件名、大小、SHA-256、签名元数据、release notes 与 rollback manifest 摘要。
- `release/checksums-<version>.sha256`：发布包、manifest、release notes、rollback manifest 以及可用时的上一稳定包校验和。
- `release/release-notes-<version>.zh-CN.md`：最小发布说明模板；正式审批时补充用户可见变更，不得加入秘密。
- `release/rollback-manifest-<version>.json`：当前版本、上一稳定版本及上一稳定包文件名、大小、SHA-256。

包内 `manifest.json` 覆盖除自身以外的每个文件，文件列表按序排列并记录大小和 SHA-256。校验器同时拒绝重复 ZIP 条目、路径穿越、未被包内清单覆盖的文件、私钥/证书容器以及常见明文凭据赋值。

## 版本一致性门禁

版本唯一来源是仓库根目录 `VERSION`。以下任一处不一致均拒绝：

1. 命令行 `-Version` 与 `VERSION`；
2. `bin/build-metadata.json` 与 `VERSION`；
3. Go 可执行文件构建信息中的 `meshlink/internal/version.Version` 和固定 `BuildTime`；
4. 包内 `VERSION`、包内 manifest、外部 manifest；
5. `meshlink-<version>.zip`、release notes 和 rollback manifest 文件名。

## 更新失败与回滚

客户端先下载到 `.download` 临时文件，只有大小、SHA-256、ZIP 结构、必需文件、包内版本、逐文件哈希和签名元数据全部通过后才替换同名缓存；失败时已有缓存保持不变。

应用脚本在停止服务和覆盖文件前再次完成相同校验。正式包还会对包内所有 EXE 执行 `Get-AuthenticodeSignature`，要求状态为 `Valid` 且签名证书指纹与包内 manifest 一致。验证完成后才把当前 `VERSION`、二进制、文档和示例配置保存到 `updates\last-known-good\<version>-<UTC时间>`。复制、服务重启或桌面重启失败时执行 `Restore-LastKnownGood`，恢复当前版本并保留备份清单供审计；真实配置、证书、邀请、设备 registry 和日志不在替换范围内。

发布侧回滚使用 `rollback-manifest-<version>.json` 指向的上一稳定 ZIP。先运行 `verify-release.ps1` 校验发布目录，再把上一稳定 ZIP 与其匹配 manifest 作为更新源；不要手工改写当前 manifest 的版本或哈希。

## 重复构建边界与签名限制

流水线承诺的是“同一批已构建/已签名二进制、同一 `BuildTime` 和相同仓库输入”的确定性打包：ZIP 条目排序固定，时间戳固定为 `2000-01-01T00:00:00Z`，完整流程会打包两次并比较 ZIP SHA-256。它不宣称两次独立 Go 编译或两次独立 Authenticode 签名必然逐字节相同；编译时间、工具链、VCS 元数据和签名随机性属于明确的重复构建边界。

当前正式门禁只验证 Windows EXE 的 Authenticode 签名，不提供 MSI/MSIX 签名，不接时间戳服务，也不管理证书签发、续期、吊销或客户端信任根分发。没有可信代码签名证书时正式发布无法完成，这是预期的 fail-closed 行为。企业离线更新的 Ed25519 清单/授权验证继续使用既有独立路径；`package-private.ps1` 仍明确标记其外部生产签名门禁，11C 不把测试密钥当作生产签名。
