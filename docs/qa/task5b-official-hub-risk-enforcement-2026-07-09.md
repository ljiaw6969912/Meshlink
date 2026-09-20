# Task 5B 官方 Hub 风控运行闭环、活跃 Relay 撤销与阶段门禁演练

日期：2026-07-09

## 交付边界

- 冻结或封禁账号会对该账号当前进程内 active/pending Relay session 执行撤销，并通过 runtime hook 关闭 broker 授权和已桥接 TCP 连接。
- 吊销单台设备只撤销包含该设备的 active/pending Relay session；同账号其他设备之间的 pending/active Relay session 不受影响，仍可完成授权加入。
- Relay quota 超限仍拒绝新建 Relay session，并记录 `quota_exceeded` 与 `relay_session_denied` 风险事件。
- 管理侧新增账号 summary，可查询账号 policy、risk events、audit events、relay usage、connection logs、active/pending/closed Relay session 摘要。
- 风控和审计事件只记录最小元数据，不记录 invite token、invite code、relay join token、private key、远程桌面内容、文件内容或剪贴板内容。
- 官方 Hub 继续不提供匿名代理、公网出口、全隧道、HTTP CONNECT、SOCKS、任意公网端口转发、公开转发节点或长期匿名中继。

## 自动化测试

- `go test ./internal/cloudhub`
  - 结果：PASS。
- `go test ./internal/relay`
  - 结果：PASS。
- `go test ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub`
  - 结果：PASS。
- `go test ./...`
  - 结果：PASS。

## 本地真实进程 smoke

启动真实 `mesh-cloudhub` 临时二进制，同时启用 HTTP 控制面和 Relay TCP listener：

```powershell
mesh-cloudhub-task5b-smoke.exe -listen 127.0.0.1:18280 -relay-listen 127.0.0.1:18281
```

演练脚本只输出脱敏阶段结果，不打印 invite token、invite code、relay join token 或私钥类字段。

验证步骤：

1. 创建账号、网络、邀请和 4 台设备，heartbeat online。
2. 创建 Relay session，两端本地 TCP 客户端握手进入 Relay，发送 `ping` 并确认目标端收到。
3. 通过 HTTP 冻结账号，确认两端 TCP 连接关闭，session 状态变为 `closed`，错误原因包含 `account frozen`。
4. 冻结后再次创建 Relay session 返回 HTTP `403`。
5. 解冻账号后创建一条设备 1/2 相关 active session，再创建一条设备 3/4 无关 pending session。
6. 通过 HTTP 吊销设备 1，确认设备 1/2 session 被关闭且错误原因包含 `device revoked`；设备 3/4 session 保持 pending，并且两端随后可以成功加入。
7. 使用已吊销设备再次创建 Relay session 返回 HTTP `403`。
8. 另建账号写入超过默认低额度的 Relay usage，再次创建 Relay session 返回 HTTP `429`。
9. 查询账号 summary，确认能看到 risk/audit/usage/log/session 摘要，且响应不包含敏感字段标记或本次 relay/invite token 值。

Smoke 输出：

```text
phase health ok
phase freeze revoke ok
phase device revoke ok
phase quota ok
SMOKE PASS freeze_disconnect=ok device_revoke_scope=ok quota_denial=ok summary_redaction=ok
```

## 风控证据

- 账号冻结路径记录 `account_frozen`、`account_enforcement_applied` 和逐 session `relay_session_revoked` 风险事件。
- 设备吊销路径记录逐 session `relay_session_revoked` 风险事件，并保持无关设备的 session 可继续加入。
- quota 路径记录 `quota_exceeded` 和 `relay_session_denied`，新建 Relay session 被 HTTP `429` 拒绝。
- Relay runtime revoke 会关闭已桥接 TCP 连接；已转发字节由 runtime close 回写到 Cloud Hub，usage 与 connection log 可通过管理 summary 查询。

## 后续事项

- Task 9 再接入真实支付、订阅回调、套餐购买页面和商业计费闭环。
- Task 10 再设计团队/组织/RBAC/企业合同和组织级风控。
- Task 6/7 再推进 P2P 直连、NAT 打洞等非 Relay 数据路径。
- Task 8/11 再推进完整 Windows RDP 官方闭环 UI；本阶段不宣称官方 Relay、真实订阅、P2P 或 Windows RDP 官方闭环已正式发布。
