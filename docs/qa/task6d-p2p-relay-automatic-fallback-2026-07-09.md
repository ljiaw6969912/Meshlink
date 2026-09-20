# Task 6D P2P Direct 失败后的 Relay 自动回退闭环

日期：2026-07-09

## 覆盖范围

- 新增 `internal/p2p` 自动 fallback 编排层：先执行 direct connector；只有在结果为 `fallback_relay` 且 `relay_fallback.create_relay_session=true` 时，才创建 Relay session 并授权 Relay runtime。
- 新增 Cloud Hub P2P Relay fallback 适配器：把 P2P 编排层的通用请求转换为既有 Cloud Hub Relay session 创建/关闭服务，并在 P2P fallback 语境下要求 source/target 设备当前 online、未 revoked。
- 复用既有 `internal/relay` TCP runtime：自动创建并授权 Relay session 后，两端通过本地 TCP relay listener 加入，完成双向字节转发。
- 结果诊断表达：已尝试 direct、direct 失败原因、已创建/授权 Relay fallback、当前路径为 `relay`。

## 自动化等价验证

- `TestAutoFallbackConnectorCreatesAndAuthorizesRelayAfterDirectFailure` 验证 direct 拒绝后自动创建并授权 Relay，结果包含 final path、direct 失败原因、Relay session id、Relay endpoint 和用户可理解诊断。
- `TestP2PAutomaticRelayFallbackRuntimeSmoke` 使用本地进程内 Cloud Hub service 与 Relay runtime，验证 direct 失败后在 15 秒门禁内进入 Relay fallback；测试采用即时失败 dialer 和短超时断言，不 sleep 15 秒。
- 同一 smoke 验证两端通过 TCP relay listener 加入后完成 source->target、target->source 双向字节转发，并确认 Relay session closed、usage rows、connection log 和 account summary 都有数据。
- `TestP2PAutomaticRelayFallbackDoesNotCreateRelayOnDirectSuccess` 验证 direct 成功时不会创建 Relay session，summary 中 Relay session 为 0，Relay usage 为 0。
- `TestAutoFallbackConnectorFailsClosedWhenFallbackDisabledOrRuntimeUnavailable`、`TestP2PAutomaticRelayFallbackDoesNotReportSuccessWhenRuntimeUnavailable` 验证 fallback 禁用或 runtime 不可用时不误报 Relay 成功。
- `TestP2PRelaySessionManagerRejectsUnsafeFallbackBoundaries` 和 `TestP2PAutomaticFallbackDoesNotReportRelaySuccessWhenCloudHubRejectsSession` 覆盖 frozen、banned、revoked、offline、Relay quota exceeded 等拒绝路径。

## 敏感字段边界

- 普通自动 fallback 结果只暴露 Relay session id、Relay endpoint、final path 和诊断，不暴露任何一次性接入凭据。
- 一次性接入凭据仅在测试进程内通过内存 grant 传给 runtime 和测试 TCP client；不打印、不写日志、不进入 QA 文档。
- JSON 断言覆盖敏感凭据、凭据哈希和密钥材料边界，并验证结果、grant 的公开 JSON、summary 不包含敏感字段或凭据明文。
- 本任务未引入匿名代理、公网出口或任意公网端口转发能力。

## 兼容性边界

- 未改变旧 `tcp_tls_v1` 自托管路径。
- 未改变既有 Relay API/runtime 的基础行为；offline 设备限制只放在 P2P fallback 的 Cloud Hub 适配器中，不全局改变 `CreateRelaySession`。
- 未实现 NAT 打洞、UDP/QUIC 或多路径切换。

## 未完成事项

- 真实 Windows RDP 数据面桥接。
- UI 展示“已通过中继连接”。
- 复杂 NAT。
- UDP/QUIC。
- 多路径热切换。
- 质量评分。

## 本地验收命令

- `go test ./internal/p2p ./internal/cloudhub ./internal/relay ./cmd/mesh-cloudhub`
- `go test ./...`
