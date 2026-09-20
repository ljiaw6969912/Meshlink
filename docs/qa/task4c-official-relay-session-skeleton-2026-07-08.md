# Task 4C 官方 Hub 基础 Relay 会话骨架 QA

日期：2026-07-08

## 当前交付边界

- Cloud Hub 新增 Relay session 控制面模型，记录 `account_id`、`network_id`、source/target device、`path_type=relay`、状态、过期时间、开始/结束时间和双向 relay 字节计数。
- 创建 Relay session 时只允许同一账号、同一官方网络内的两台未 revoked 设备；账号 banned、网络不属于账号、设备跨网络、设备 revoked、source/target 为空都会拒绝。
- 创建响应会短期返回 source/target 一次性 join token；服务端持久 session 和 audit 只保存 join token hash，不保存明文 token、code、password 或私钥。
- `internal/relay` 新增内存 broker 骨架：只接受已授权 session 的 source/target 两端按角色和 token hash 加入；两端匹配后返回一对 `net.Conn`，并统计 source->target 与 target->source 字节数。
- Broker 具备错误 token、重复加入、过期、吊销、单端等待超时等基础保护。
- HTTP API 覆盖创建、查询、记录 usage 和关闭 Relay session：`POST /api/relay/sessions`、`GET /api/relay/sessions/{id}`、`POST /api/relay/sessions/{id}/usage`、`POST /api/relay/sessions/{id}/close`。

## 明确不包含

- 不提供 HTTP CONNECT、SOCKS、匿名代理、公网出口、全隧道或任意公网端口转发。
- 不提供“拨号到任意公网地址”的数据面能力。
- 不声明官方 Relay、真实订阅、P2P 或 Windows RDP 官方闭环已经正式可用。
- 当前 broker 是库级内存骨架，尚未挂载生产监听器、TLS/mTLS、长期持久化、跨进程调度或多节点 Relay 集群。

## 本地测试方法

必跑：

```powershell
go test ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub
go test ./...
```

进程级 smoke：

```powershell
go build -o $env:TEMP\mesh-cloudhub-task4c.exe .\cmd\mesh-cloudhub
$proc = Start-Process -FilePath $env:TEMP\mesh-cloudhub-task4c.exe -ArgumentList "-listen", "127.0.0.1:18081" -PassThru -WindowStyle Hidden
# 使用 HTTP API 完成 account -> network -> invite -> 两台设备 join -> heartbeat online -> 创建 relay session -> usage -> close
Stop-Process -Id $proc.Id
```

Broker 数据面 smoke：

```powershell
go test ./internal/relay -run TestBrokerRelaysBidirectionalBytesAndReportsClose -count=1
```

## 后续事项

- 将 broker 与 Cloud Hub session 生命周期正式接线：创建授权、匹配后置 active、关闭回调写回 Cloud Hub usage/log。
- 增加真实数据面监听入口和传输安全设计，但仍必须保持“只服务已授权官方网络设备对”的边界。
- 增加持久化 store、session 清理任务、审计查询和运维指标。
- 后续 UI 文案仍需保持“基础 Relay 骨架/内测”，避免误导用户认为完整官方 Relay 或 RDP 闭环已发布。
