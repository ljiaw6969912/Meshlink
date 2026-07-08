# Meshlink 自建中继运维手册

本文档对应产品改造 Task 3：用户有一台 Linux 云服务器时，桌面端通过 SSH 自动部署公网 Meshlink Hub。该 Hub 只服务用户自己的私有远程桌面组网，不是官方 Hub、匿名代理、公网出口或公开转发节点。

## 部署模型

远程 Linux 云服务器固定使用以下模型：

| 项目 | 路径或名称 |
| --- | --- |
| systemd 服务名 | `meshlink-agent` |
| systemd unit | `/etc/systemd/system/meshlink-agent.service` |
| 二进制 | `/opt/meshlink/bin/mesh-agent` |
| 配置根目录 | `/etc/meshlink` |
| Hub 配置 | `/etc/meshlink/configs/active.json` |
| CA 证书 | `/etc/meshlink/certs/ca.pem` |
| CA 私钥 | `/etc/meshlink/certs/ca-key.pem` |
| Hub 证书 | `/etc/meshlink/certs/meshlink-hub.pem` |
| Hub 私钥 | `/etc/meshlink/certs/meshlink-hub-key.pem` |
| 邀请文件 | `/etc/meshlink/invites/invites.json` |
| 运行状态 | `/etc/meshlink/configs/logs/mesh-agent.status.json` |
| systemd 日志 | `journalctl -u meshlink-agent` |
| 部署诊断日志 | `/var/log/meshlink/deploy-last-status.log`、`/var/log/meshlink/deploy-last-journal.log` |
| 回滚备份 | `/opt/meshlink/backups/<UTC 时间戳>/` |

文件权限：

- `/opt/meshlink/bin/mesh-agent`: `0755`
- `/etc/meshlink/configs/active.json`: `0600`
- `/etc/meshlink/certs/ca.pem`: `0644`
- `/etc/meshlink/certs/ca-key.pem`: `0600`
- `/etc/meshlink/certs/meshlink-hub.pem`: `0644`
- `/etc/meshlink/certs/meshlink-hub-key.pem`: `0600`
- `/etc/meshlink/invites/invites.json`: `0600`

## 自动部署流程

桌面端向导执行以下步骤：

1. SSH 连接云服务器，支持密码或私钥。
2. 先执行“检查云服务器”记录并展示首次主机指纹；部署阶段只接受已记录且匹配的指纹。
3. 检查远程系统为 Linux。
4. 检查 `systemctl` 和 `/run/systemd/system`。
5. 检查当前用户为 root 或具备免密 sudo。
6. 检查 Meshlink 监听端口未被其他进程占用；已有 `meshlink-agent` 正在升级时允许继续。
7. 检查用户填写的公网访问地址在本机没有被代理 TUN/fake-ip 解析到 `198.18.0.0/15`；如果命中 fake-ip，部署会在上传前停止并提示配置 DIRECT 和 fake-ip-filter。
8. 在本机临时生成 Hub 配置、CA、Hub 证书和长期接入码。
9. 上传 Linux `mesh-agent`、配置、证书、邀请文件和 systemd unit。
10. 创建远程备份。
11. `systemctl daemon-reload`、`systemctl enable meshlink-agent`、`systemctl restart meshlink-agent`。
12. 在远端检查 `systemctl is-active meshlink-agent`、监听端口和 `https://127.0.0.1:<端口>/enroll/health`。
13. 在桌面端本机检查用户填写的公网访问地址：`https://<公网地址>:<端口>/enroll/health`。
14. 返回远程服务状态、主机指纹、接入链接、接入码、有效期和健康检查结果。

SSH 密码、SSH 私钥、CA 私钥内容不会写入本地配置、known-hosts 文件或审计日志。

## 手工安装

在本机先生成 Linux agent：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\build-linux-agent.ps1
```

把以下文件复制到云服务器对应路径：

```text
bin\linux\mesh-agent -> /opt/meshlink/bin/mesh-agent
临时生成的 active.json -> /etc/meshlink/configs/active.json
临时生成的 certs\* -> /etc/meshlink/certs/
临时生成的 invites\invites.json -> /etc/meshlink/invites/invites.json
```

设置权限：

```bash
sudo chmod 0755 /opt/meshlink/bin/mesh-agent
sudo chmod 0600 /etc/meshlink/configs/active.json
sudo chmod 0644 /etc/meshlink/certs/ca.pem /etc/meshlink/certs/meshlink-hub.pem
sudo chmod 0600 /etc/meshlink/certs/ca-key.pem /etc/meshlink/certs/meshlink-hub-key.pem
sudo chmod 0600 /etc/meshlink/invites/invites.json
```

安装 systemd 服务：

```bash
sudo cp scripts/linux-systemd.sh /tmp/linux-systemd.sh
sudo sh /tmp/linux-systemd.sh install-service
```

检查状态：

```bash
systemctl is-active meshlink-agent
sudo /opt/meshlink/bin/mesh-agent -version
systemctl status meshlink-agent --no-pager
journalctl -u meshlink-agent -n 120 --no-pager
curl -kfsS https://127.0.0.1:8443/enroll/health
```

在桌面端或任意客户端网络检查公网入口：

```powershell
curl.exe -k https://<公网地址或域名>:8443/enroll/health
```

真实云主机 E2E 建议使用脚本交互式输入 SSH 密码，避免把密码写进命令历史：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\e2e-self-relay.ps1 `
  -SshHost <SSH 可达地址> `
  -SshPort 22 `
  -SshUser root `
  -PublicAddress <公网地址或域名>:8443 `
  -ListenPort 8443
```

## 升级

自动升级会先把当前版本备份到 `/opt/meshlink/backups/<UTC 时间戳>/`，再覆盖二进制、配置、证书、邀请文件和 service unit。手工升级步骤：

```bash
backup="/opt/meshlink/backups/$(date -u +%Y%m%dT%H%M%SZ)"
sudo mkdir -p "$backup"
sudo cp -a /opt/meshlink/bin "$backup/bin"
sudo cp -a /etc/meshlink "$backup/config-root"
sudo cp -a /etc/systemd/system/meshlink-agent.service "$backup/service"

sudo install -m 0755 mesh-agent /opt/meshlink/bin/mesh-agent
sudo systemctl daemon-reload
sudo systemctl restart meshlink-agent
systemctl is-active meshlink-agent
```

## 重启

```bash
sudo systemctl restart meshlink-agent
systemctl is-active meshlink-agent
curl -kfsS https://127.0.0.1:8443/enroll/health
```

## 回滚

自动部署在服务启动失败时会采集诊断日志，然后恢复上一版二进制、配置和 service unit。手工回滚：

```bash
backup="/opt/meshlink/backups/<UTC 时间戳>"
sudo rm -rf /opt/meshlink/bin
sudo cp -a "$backup/bin" /opt/meshlink/bin
sudo rm -rf /etc/meshlink
sudo cp -a "$backup/config-root" /etc/meshlink
sudo cp -a "$backup/service" /etc/systemd/system/meshlink-agent.service
sudo systemctl daemon-reload
sudo systemctl restart meshlink-agent
systemctl is-active meshlink-agent
```

## 卸载

卸载不会自动删除证书和邀请文件；确认不再需要后再手动删除 `/etc/meshlink`。

```bash
sudo systemctl stop meshlink-agent || true
sudo systemctl disable meshlink-agent || true
sudo rm -f /etc/systemd/system/meshlink-agent.service
sudo systemctl daemon-reload
sudo rm -rf /opt/meshlink
sudo rm -rf /var/log/meshlink
```

## 排障

| 问题 | 检查项 | 处理 |
| --- | --- | --- |
| SSH 网络不可达 | 云服务器 IP、SSH 端口、安全组、本机网络 | 放行 SSH 端口，确认云服务器公网地址正确 |
| SSH 认证失败 | 用户名、密码、私钥格式、云厂商禁用密码登录策略 | 改用正确私钥或启用对应登录方式 |
| 主机指纹变化 | 云服务器是否重装、IP 是否复用、DNS 是否指向错误机器 | 确认安全后删除本机 `configs/ssh_known_hosts.json` 中对应记录 |
| 权限不足 | `id -u`、`sudo -n true` | 使用 root 登录或配置免密 sudo |
| systemd 不存在 | `command -v systemctl`、`test -d /run/systemd/system` | 更换 systemd Linux 发行版 |
| 端口被占用 | `ss -ltn` 或 `netstat -ltn` | 更换 Meshlink 监听端口或停止占用进程 |
| 服务启动失败 | `systemctl status meshlink-agent`、`journalctl -u meshlink-agent -n 120` | 查看 `/var/log/meshlink/deploy-last-*.log`，修正配置后重启 |
| 远端 `/enroll/health` 不可访问 | 远端 `curl -k https://127.0.0.1:<端口>/enroll/health`、`ss -ltnp` | 确认服务运行、监听 `0.0.0.0:<端口>` |
| 公网接入接口不可访问 | 桌面端 `curl -k https://<公网地址>:<端口>/enroll/health`、云安全组、路由器端口映射、DNS 是否为 fake-ip | 放行 TCP 监听端口；把公网端口转发到 Linux VM 的内网 IP；代理环境下为接入域名配置 DIRECT 和 fake-ip-filter，或直接使用真实公网 IP |

## 本机加入

Task 3 当前交付接入链接和接入码。桌面端本机加入该中继应复用现有 `JoinSpoke` 流程，spoke 私钥仍只在本机生成；自动加入动作留作后续小任务，避免部署远程 Hub 时把本机服务安装/重启流程耦合进去。
