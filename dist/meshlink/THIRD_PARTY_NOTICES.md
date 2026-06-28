# Third-Party Notices

本文件列出 Meshlink 随包引用、依赖或分发的主要第三方组件。第三方组件的版权、商标和许可条款归其各自权利人所有。本项目的专有许可证仅适用于 Meshlink 项目自身拥有权利的代码、文档、配置、界面和相关资源，不改变第三方组件原有许可证授予的权利或限制。

## Wintun

Meshlink 的 Windows 版本可能随包附带 `wintun.dll`，用于创建和访问 Windows TUN 虚拟网卡。

组件信息：

- 文件路径：`bin\wintun.dll`
- 文件版本：`0.14.1`
- 产品名称：`Wintun Driver`
- 权利人：`WireGuard LLC`
- 签名主体：`WireGuard LLC`
- SHA256：`E5DA8447DC2C320EDC0FC52FA01885C103DE8C118481F683643CACC3220DAFCE`
- 官方网站：`https://www.wintun.net/`
- 官方源码仓库：`https://git.zx2c4.com/wintun/`

版权与许可说明：

Wintun 是 WireGuard LLC 的第三方组件，不属于 Meshlink 原创代码。Wintun 源代码采用 GPLv2 发布；官方预编译并签名的 `wintun.dll` 使用其官方发布包中附带的预编译二进制许可。根据 Wintun 官方说明，预编译签名 DLL 使用比 GPLv2 更宽松的许可，适用于更多软件分发场景。

Meshlink 仅按原样随包分发官方预编译的 `wintun.dll`，用于实现 Windows TUN 虚拟网卡功能。Meshlink 不对 `wintun.dll` 进行修改、反编译、逆向工程、重新签名或声称拥有其版权。

使用者、分发者或商业部署方在包含、复制、分发或使用 `wintun.dll` 时，应自行确认并遵守 Wintun 官方许可条款，包括官方发布包中附带的预编译二进制许可文件。

本项目与 WireGuard LLC、WireGuard 项目及 Wintun 项目不存在从属、合作、赞助、认证或官方背书关系。WireGuard、Wintun 及相关名称、标识和商标归其各自权利人所有。

## Go Dependencies

Meshlink 还可能依赖 Go 模块、系统库和其他运行时组件。相关组件的具体版本以 `go.mod`、`go.sum` 和实际构建产物为准。各依赖组件的版权和许可条款归其各自权利人所有，并按其原始许可证执行。
