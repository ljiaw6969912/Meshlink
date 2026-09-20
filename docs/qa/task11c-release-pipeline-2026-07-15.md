# Task 11C 发布流水线 QA 记录（2026-07-15）

## 范围与结论

- 范围：只完成 Task 11.3（11C）发布流水线；未实现 Task 11.4/11.5，未接 CI 厂商、制品库、在线签名、部署平台或时间戳服务。
- 共享工作区：直接在 `main` 工作区完成；未撤销 11A/11B 并行改动，未暂存、未提交。
- 结果：开发模式完整测试、构建、确定性双打包、manifest/ZIP/校验和/秘密/rollback/release notes 校验通过；正式模式缺签名配置会在构建前 fail-closed。
- 签名判定：本次只生成 `development`、`code_signed=false` 的 unsigned 开发制品，不构成生产代码签名验收。

## TDD 证据

先补测试并观察以下预期 RED，再写最小实现转 GREEN：

1. 在线 manifest 错误接受任意 ZIP 文件名、空/畸形 SHA-256、unsigned release、缺证书身份 release、伪装成 signed 的 development。
2. 下载器用缺必需文件的 ZIP 覆盖已有 known-good 缓存。
3. 应用脚本缺包内 manifest/逐文件哈希/Authenticode/last-known-good/恢复门禁。
4. 企业离线更新文件名未绑定版本、平台和架构。
5. `publish-update.bat` 缺 `-Help`、绕过固定发布入口并写 `VERSION`。
6. 反斜杠 ZIP 条目被 Windows 路径正规化后错误接受。
7. 失败恢复时服务可能仍运行，导致 last-known-good 覆盖失败。
8. rollback manifest 可把高于当前版本的包标记为 previous。

脚本专项还固定验证四类故意失败：包 SHA-256 篡改、重复打包字节差异、正式 unsigned、rollback previous 不小于 current。

## 最终成功用例

### 更新模块专项

```text
go test -count=1 ./internal/update
ok meshlink/internal/update
```

覆盖严格 manifest、版本/文件名/SHA-256/大小、ZIP 路径与重复条目、必需文件、包内逐文件哈希、秘密扫描、签名元数据、离线 Ed25519 签名与绑定、known-good 缓存及应用脚本回滚顺序。

### PowerShell 与批处理专项

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\test-release-pipeline.ps1
Task 11C release pipeline script tests passed.

cmd /d /c call publish-update.bat -Help
exit 0
```

专项脚本通过 PowerShell Parser 检查 `build.ps1`、`package.ps1`、`release.ps1`、`verify-release.ps1` 和自身语法；批处理 `-Help` 以退出码 0 返回，未构建、未写版本、未安装服务。

### 非发布构建与打包

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\build.ps1 -Mode Development
Built Meshlink 0.1.5 (development)

powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\package.ps1 -Mode Development
Verified Meshlink development release 0.1.5
```

`build.ps1` 内 fresh 执行完整 `go test -count=1 ./...`，所有 Go 包通过；随后生成五个 Windows EXE、Linux agent 和 `bin/build-metadata.json`。`package.ps1` 校验 Go build info 中的内部版本/构建时间、构建元数据哈希、必需文件和秘密，再生成确定性 ZIP 与发布侧产物。

### 完整开发发布流水线

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release.ps1 -Mode Development
Meshlink Development release pipeline completed for 0.1.5
exit 0
```

最终一次完整流程再次 fresh 执行 `go test -count=1 ./...`，然后对同一构建输入打包两次。两次 `meshlink-0.1.5.zip` 的 SHA-256 完全一致：

```text
5ffe9edbc486b096785e30f184941c56eda919d632c5e3e66f4640ac1f7e66e7
```

最终审计值：

| 字段 | 值 |
| --- | --- |
| `VERSION` / build metadata / manifest / ZIP 文件名 | `0.1.5` |
| 固定 BuildTime | `2026-07-15T06:13:37Z` |
| 模式 | `development` |
| 签名 | `code_signed=false`，证书指纹为空 |
| `release/meshlink-0.1.5.zip` | `5ffe9edbc486b096785e30f184941c56eda919d632c5e3e66f4640ac1f7e66e7` |
| `dist/meshlink.zip` | 与 release ZIP 完全相同 |
| 重复打包 | 同一 SHA-256，通过 |
| 临时目录 | `meshlink-task11c-*`、`meshlink-release-repeat-*` 均已清理 |

开发模式的 `rollback-manifest-0.1.5.json` 允许 `package=null`，用于不带生产上一版本包的流程验证；客户端应用仍会创建 last-known-good。正式模式不允许该状态，必须提供已校验的上一稳定包。

## 故意失败门禁

### 正式模式缺签名配置

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\release.ps1 -Mode Release -Version 0.1.5
exit 1
Formal release requires MESHLINK_SIGNING_CERT_THUMBPRINT or -SigningCertificateThumbprint
```

失败发生在测试和构建前，未生成测试证书、未签名、未降级为 unsigned release。

### 命令版本与唯一版本源不一致

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\package.ps1 -Mode Development -Version 9.9.9
exit 1
Requested version '9.9.9' does not match VERSION '0.1.5'
```

### 外部 manifest 与期望版本不一致

```text
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-release.ps1 -ReleaseDir .\release -Version 9.9.9 -ExpectedMode Development
exit 1
Release manifest schema or version mismatch
```

专项测试另确认：ZIP 篡改后 SHA-256 门禁失败；ReferencePackagePath 与主包字节不同时重复打包门禁失败；release 模式 `code_signed=false` 失败；previous version 高于 current version 失败。

## 更新与回滚行为

- `Check` 对新 schema 严格检查版本、固定包文件名、64 位十六进制 SHA-256、正大小、模式和签名元数据；legacy manifest 只保留兼容读取，不得携带新签名字段。
- `DownloadPackage` 先写 `.download`，验证失败删除临时文件，不替换同名 known-good 缓存；拒绝路径穿越、反斜杠、重复条目、缺必需文件、包内版本/哈希/签名元数据不一致和秘密。
- 应用脚本在停止服务前完成包内全部校验；Release 对所有 EXE 验证 Authenticode 状态和证书指纹。
- 覆盖前把当前 `VERSION`、二进制、文档和示例配置保存到 `updates\last-known-good`；失败时先停原服务、恢复备份，再按原状态重启。
- 配置、证书、邀请、设备 registry 和日志不在替换范围。
- 企业离线更新继续验证 Ed25519 签名、授权/部署绑定、版本顺序、文件名、大小和 SHA-256；失败不覆盖已暂存 known-good 包。

## 已知签名限制

- 正式签名要求执行账号的 Windows `CurrentUser\My` 证书存储中已有受控代码签名证书和私钥；仓库不提供证书、私钥或密码。
- 仅校验 Windows EXE 的 Authenticode；未实现 MSI/MSIX 签名、证书签发/续期/吊销、客户端信任根分发或在线时间戳。
- 未接在线签名服务，因此没有时间戳的 Authenticode 签名在证书到期后的长期有效性受限；正式上线前需由组织外部签名运维流程补足。
- 重复构建保证限于同一批已构建/已签名二进制、同一 BuildTime 和相同输入的确定性打包，不承诺两次独立编译或独立签名字节相同。
- `package-private.ps1` 的企业包仍保留既有外部生产签名门禁并明确 `code_signed=false`；11C 未把测试密钥或离线授权签名冒充为代码签名。
