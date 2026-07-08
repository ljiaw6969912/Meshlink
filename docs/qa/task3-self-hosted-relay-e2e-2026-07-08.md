# Task 3 自建中继手工 E2E 记录

日期：2026-07-08

状态：实验室 E2E 通过，公网入口健康检查通过，完整公网 E2E 通过。已在用户提供的 Linux VM 上完成真实远端部署、`/enroll/health`、两客户端加入和远端设备登记验证；公网 `175.42.58.100:8443` 已可访问 `/enroll/health` 并可作为接入链接服务器地址。

## 本次真实环境

- SSH 目标：`175.42.58.100:22`，root 用户。
- 用户提供域名：`openwrt.<redacted>.top`。
- DoH 解析结果：`175.42.58.100`。
- 本机普通 DNS/代理解析结果：`198.18.0.146`，属于代理 fake-ip 网段，不能作为 Meshlink 接入地址。
- 远端 VM：Linux x86_64，systemd 可用，`/dev/net/tun` 可用。
- 远端 VM 内网地址：`192.168.1.32`，上级网关：`192.168.1.1`。
- Meshlink 监听端口：`8443`。
- 实验室接入地址：`192.168.1.32:8443`。
- 公网接入地址：`175.42.58.100:8443`。

## 需要的环境

- 一台公网可访问的 Linux 云服务器，使用 systemd。
- 云服务器 SSH 用户为 root，或具备免密 sudo。
- 云服务器安全组放行 SSH 端口和 Meshlink 监听端口。
- 两台无公网 Windows 设备，均可运行 `mesh-desktop.exe`。
- 本地已运行：
  - `powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1`
  - 生成 `bin\linux\mesh-agent`

## 验证步骤

1. 在桌面端打开“我有云服务器，帮我自建中继”。
2. 输入云服务器地址、SSH 端口、用户名、密码或私钥、Meshlink 监听端口、服务器公网访问地址。
3. 点击“检查云服务器”，记录主机指纹。
4. 确认主机指纹可信后点击“部署自建中继”。
5. 确认界面显示：
   - 远程服务状态：`running`
   - 主机指纹
   - 接入链接
   - 接入码
   - 有效期或长期有效
6. 在云服务器执行：
   ```bash
   systemctl status meshlink-agent --no-pager
   /opt/meshlink/bin/mesh-agent -version
   curl -kfsS https://127.0.0.1:<Meshlink 监听端口>/enroll/health
   ```
7. 在桌面端所在机器执行公网入口检查：
   ```powershell
   curl.exe -k https://<服务器公网访问地址>:<Meshlink 监听端口>/enroll/health
   ```
8. 在第一台 Windows 设备使用接入链接和接入码加入。
9. 在第二台 Windows 设备使用同一接入链接和接入码加入。
10. 在设备列表确认两台客户端出现在同一云服务器 Hub 下。
11. 使用虚拟 IP 执行远程桌面或 RDP 诊断。

## 本次执行结果

- `go test -tags e2e ./internal/onboarding -run TestE2ESelfHostedRelayDeploysAndEnrollsTwoClients -v -count=1`
  - 使用 `openwrt.<redacted>.top:2858`：SSH banner 阶段 `EOF`。后续确认本机 DNS 将域名解析到 `198.18.0.146` fake-ip，且 `2858` 当前不返回 SSH banner。
  - 使用 `175.42.58.100:22`：SSH 指纹登记成功，远端部署成功，远端 health 通过。
  - 公网入口检查失败：`https://175.42.58.100:8443/enroll/health` 从本机访问返回 TLS EOF。
  - 补充公网 health 校验后重新执行，失败信息为：`health_check：公网接入接口不可访问 175.42.58.100:8443`，并提示检查公网访问地址、安全组、防火墙和路由器端口映射。
  - 使用实验室桥接地址 `192.168.1.32:8443` 重新执行：通过。
    - 公网入口替代检查：`https://192.168.1.32:8443/enroll/health` 返回 ok。
    - 接入链接：`meshlink://join?...server=192.168.1.32%3A8443...`
    - 接入码：已生成 6 位接入码，长期有效，最大 10 次。
    - 客户端 1 加入：`e2e-home-*`，虚拟 IP `10.77.0.2`。
    - 客户端 2 加入：`e2e-away-*`，虚拟 IP `10.77.0.3`。
    - 测试读取远端 `/etc/meshlink/configs/devices.json`，确认两个客户端节点均已登记。
  - 补充 fake-ip 上传前拦截后再次使用 `192.168.1.32:8443` 重跑：通过。
    - 公网入口替代检查：`https://192.168.1.32:8443/enroll/health` 返回 ok。
    - 新客户端虚拟 IP：`10.77.0.4`、`10.77.0.5`。
    - 测试再次读取远端设备登记，确认新加入的两个客户端节点均存在。
  - 路由器端口映射修正后，使用安全输入脚本重新执行公网地址 `175.42.58.100:8443`：通过。
    - SSH 主机指纹登记成功。
    - 公网入口检查：`https://175.42.58.100:8443/enroll/health` 返回 ok。
    - 接入链接服务器地址：`175.42.58.100:8443`。
    - 接入码：已生成 6 位接入码，长期有效，最大 10 次。
    - 客户端 1 加入：`e2e-home-*`，最新虚拟 IP `10.77.0.8`。
    - 客户端 2 加入：`e2e-away-*`，最新虚拟 IP `10.77.0.9`。
    - 测试读取远端 `/etc/meshlink/configs/devices.json`，确认两个公网接入客户端节点均已登记。
    - 测试临时启动两个本地 `mesh-agent.exe` 客户端控制连接，并读取远端 `/etc/meshlink/configs/logs/mesh-agent.status.json`，确认两个公网接入客户端状态为 `online`。
- 远端诊断：
  ```text
  systemctl is-active meshlink-agent -> active
  ss -ltnp -> mesh-agent 监听 0.0.0.0:8443
  curl -k https://127.0.0.1:8443/enroll/health -> {"ok":true,"protocol":"tcp_tls_v1"}
  active.json listen -> 0.0.0.0:8443
  ```
- 远端权限验证：
  ```text
  /opt/meshlink/bin/mesh-agent -> 0755 root:root
  /etc/meshlink/configs/active.json -> 0600 root:root
  /etc/meshlink/certs/ca.pem -> 0644 root:root
  /etc/meshlink/certs/ca-key.pem -> 0600 root:root
  /etc/meshlink/certs/meshlink-hub.pem -> 0644 root:root
  /etc/meshlink/certs/meshlink-hub-key.pem -> 0600 root:root
  /etc/meshlink/invites/invites.json -> 0600 root:root
  /etc/systemd/system/meshlink-agent.service -> 0644 root:root
  ```
- 本地敏感串扫描：
  ```text
  rg -n "<用户提供的 SSH 密码或完整私有域名>" docs internal README.md cmd scripts -S -> 无命中
  ```
- VM 内部访问公网地址：
  ```text
  curl -k https://175.42.58.100:8443/enroll/health -> Connection refused
  ip route get 175.42.58.100 -> via 192.168.1.1 dev ens33 src 192.168.1.32
  ```
- 外网客户端访问公网地址时的抓包证据：
  ```text
  外网 curl -k -v https://175.42.58.100:8443/enroll/health -> schannel TLS handshake failed
  VM 上 tcpdump -ni ens33 'tcp port 8443' ->
    175.42.58.165:* > 192.168.1.23:8443 Flags [S]
    192.168.1.23:8443 > 175.42.58.165:* Flags [R.]
  ```
- 结论：公网 `8443` 当前实际被路由器转发到了 `192.168.1.23:8443`，不是 Linux VM `192.168.1.32:8443`。Windows 主机 `192.168.1.23` 对 `8443` 返回 RST，因此外网客户端看到 TLS handshake failed。
- 路由器修正后公网入口检查：
  ```text
  curl -k -v https://175.42.58.100:8443/enroll/health -> HTTP/1.1 200 OK
  body -> {"ok":true,"protocol":"tcp_tls_v1"}
  ```
- 判断：Meshlink Hub 已在 VM 上运行；产品部署、接入链接、接入码、两客户端加入和远端设备登记已通过实验室桥接地址和公网地址双路径验证。公网 `8443` 已能访问 Linux VM 上的 Meshlink Hub，并且公网接入链接可让两个客户端加入同一 Hub。
- 已补充产品逻辑：
  - 上传前会检查用户填写的公网访问地址是否被本机代理 TUN/fake-ip 解析到 `198.18.0.0/15`，命中时提示配置 DIRECT 和 fake-ip-filter。
  - 部署成功后，桌面端会从用户填写的公网访问地址检查 `/enroll/health`，失败时返回“公网接入接口不可访问”，提示检查安全组、防火墙和路由器端口映射。

## 复验命令

使用安全方式输入 SSH 凭据后可重新运行：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\e2e-self-relay.ps1 `
  -SshHost 192.168.1.32 `
  -SshPort 22 `
  -SshUser root `
  -PublicAddress 175.42.58.100:8443 `
  -ListenPort 8443
```

该脚本会重新部署公网 Hub，生成公网接入链接，加入两个临时客户端，并读取远端设备登记表确认两台客户端存在。

## 预期结果

- `systemctl status meshlink-agent` 显示 running/active。
- `/enroll/health` 返回 `ok: true`。
- 第一台和第二台客户端均能加入同一 Hub。
- 设备列表能看到在线设备和虚拟 IP。
- SSH 密码、SSH 私钥、CA 私钥内容不出现在本地审计日志、UI 错误、远程诊断日志或 known-hosts 文件中。
- 主机指纹变化时，部署被阻止。

## 失败采集项

- 桌面端错误文案截图。
- `configs/ssh_known_hosts.json` 中对应服务器指纹。
- 云服务器：
  ```bash
  systemctl status meshlink-agent --no-pager
  journalctl -u meshlink-agent -n 120 --no-pager
  ls -l /opt/meshlink/bin /etc/meshlink/configs /etc/meshlink/certs /etc/meshlink/invites /var/log/meshlink
  ```
- 本地：
  ```powershell
  go test ./internal/deployssh ./internal/onboarding ./internal/ui ./cmd/mesh-desktop
  go test ./...
  powershell -ExecutionPolicy Bypass -File .\scripts\build.ps1
  ```
