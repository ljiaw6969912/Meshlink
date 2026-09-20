# Task 4D 官方 Hub Relay runtime 监听与会话生命周期接线 QA

## 范围

- `mesh-cloudhub` 现在可以用同一个 `cloudhub.Service` 同时启动 HTTP 控制面和受控 Relay TCP 数据监听。
- HTTP 创建 Relay session 后，控制面把 session 元数据和一次性 source/target join token 授权给本进程内 relay broker；响应会返回非敏感的 `relay_endpoint`。
- source/target 两端通过 Relay TCP listener 加入后，runtime 将 session 置为 `active`；两端断开后，broker close report 会写回 Cloud Hub session bytes、connection log 和 endpoint usage，并将 session 置为 `closed`。
- `/healthz` 返回 relay listener 是否启用和监听地址，不返回 invite token、invite code、join token、私钥或 token hash。

## 启动方式

默认只启动本地 HTTP 控制面，不启用 Relay 数据监听：

```powershell
mesh-cloudhub.exe -listen 127.0.0.1:18080
```

启用本地 Relay 数据监听：

```powershell
mesh-cloudhub.exe -listen 127.0.0.1:18081 -relay-listen 127.0.0.1:18082
```

`-relay-listen` 为空时数据面关闭；启用时建议继续绑定 `127.0.0.1` 或受控内网地址。

## TCP 握手协议

Relay listener 只接受一行 JSON 握手，字段如下：

```json
{"session_id":"...","role":"source","device_id":"...","token":"..."}
```

- `role` 只能是 `source` 或 `target`。
- `device_id` 必须匹配创建 session 时的 source/target 设备。
- `token` 必须匹配对应角色的一次性 join token；服务端只保存 token hash。
- 服务端返回 `{"ok":true}` 后进入 raw stream 转发；失败返回 `{"ok":false,"error":"..."}` 后关闭连接。
- 两端匹配后只在该 session 的 source/target 之间全双工转发，并统计 source->target 与 target->source 字节数。

## Smoke 步骤

本地进程级 smoke 已执行：

1. 构建 `mesh-cloudhub` 临时二进制。
2. 启动 HTTP 控制面和本地 Relay TCP listener。
3. 通过真实 HTTP API 完成 account -> network -> invite -> 两台设备 join -> heartbeat online -> create relay session。
4. 用两个本地 TCP 客户端分别作为 source/target 发送 JSON 握手。
5. 通过 Relay 完成 `ping`/`pong` 双向数据交换。
6. 关闭两端连接后，通过 HTTP API 查询 session 为 `closed`，且 bytes in/out 大于 0。
7. 通过 HTTP API 查询 connection log 和 relay usage，确认已记录。
8. 负面 smoke：错误 join token 被 Relay listener 拒绝。

## 已运行验证

```powershell
go test ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub
go test ./...
go build -o "$env:TEMP\mesh-cloudhub-task4d.exe" .\cmd\mesh-cloudhub
go run "$env:TEMP\mesh-cloudhub-task4d-smoke.go" -bin "$env:TEMP\mesh-cloudhub-task4d.exe"
```

结果均通过，smoke 输出 `SMOKE OK`。

## 安全边界

- Relay 只允许同一官方网络内、由 Cloud Hub 控制面授权的 source/target 设备使用。
- Relay listener 不支持拨号到任意公网地址，不提供公网出口、匿名代理、全隧道、HTTP CONNECT、SOCKS 或任意公网端口转发。
- 当前仅是基础 Relay 内测/最小闭环，不声明官方 Relay、真实订阅、P2P 或 RDP 官方闭环正式可用。
- 响应、health、audit、connection log 和文档均不写入 invite token、invite code、join token 或私钥。

## 后续事项

- 当前 runtime 是单进程内存 broker，尚未实现跨进程/多节点 Relay 调度、持久化恢复或集群路由。
- 当前 TCP listener 尚未加 TLS/mTLS；生产化前需要传输层加密、证书轮换和更细粒度审计策略。
- HTTP 主动 close 活跃 session 时会撤销 broker 授权；后续可增强主动 close 与最终 byte report 的合并策略。
- 后续 UI/桌面文案仍需保持“基础 Relay 内测/最小闭环”，避免误导用户认为完整官方 Relay 或 RDP 闭环已发布。
