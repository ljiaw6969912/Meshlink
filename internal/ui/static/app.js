const $ = (id) => document.getElementById(id);

const state = {
  cwd: "",
  invite: null,
  officialInvite: null,
  officialTeam: null,
  officialRollouts: [],
  officialDeploymentObjectURLs: [],
  officialAuditCursor: "",
  officialAuditNextCursor: "",
  officialAuditHistory: [],
  officialHubState: null,
  subscriptionExperience: null,
  inviteTimer: null,
  officialHubEnabled: false,
  statusPolling: false,
  statusPollTimer: null,
  networkState: "",
  coordinatorState: "disconnected",
  p2pListen: "",
  devices: [],
  selectedDeviceKey: "",
  selectedDevice: null,
};

function setBusy(button, busy) {
  if (button) button.disabled = busy;
}

function toast(message, error = false) {
  const el = $("toast");
  el.textContent = message;
  el.className = error ? "show error" : "show";
  window.clearTimeout(toast.timer);
  toast.timer = window.setTimeout(() => {
    el.className = "";
  }, 3200);
}

async function api(path, options = {}) {
  const res = await fetch(path, {
    headers: {
      "Content-Type": "application/json",
      ...(options.headers || {}),
    },
    ...options,
  });
  const data = await res.json();
  if (!res.ok || data.ok === false) {
    const err = new Error(data.error || `Request failed: ${res.status}`);
    if (data.quota) err.quota = data.quota;
    throw err;
  }
  return data;
}

function serviceStateText(status) {
  if (!status.installed) return "未安装";
  switch (status.state) {
    case "running":
      return "运行中";
    case "stopped":
      return "已停止";
    case "start pending":
      return "正在启动";
    case "stop pending":
      return "正在停止";
    case "paused":
      return "已暂停";
    default:
      return status.state || "未知";
  }
}

function tunnelStatusText(status, kind) {
  if (kind === "self") return "本机";
  if (status === "online") return "online";
  if (status === "offline") return "offline";
  if (status === "disabled") return "disabled";
  return status || "未知";
}

function actionText(action) {
  return {
    install: "安装",
    start: "启动",
    stop: "停止",
    uninstall: "卸载",
  }[action] || action;
}

function statusText(status) {
  return {
    ok: "正常",
    warn: "提醒",
    fail: "失败",
  }[status] || status;
}

function deviceStatusText(status, kind) {
  if (kind === "self" && status === "online") return "本机在线";
  if (kind === "self") return "本机离线";
  return {
    online: "在线",
    offline: "离线",
    disabled: "已禁用",
    revoked: "已吊销",
  }[status] || status || "未知";
}

function networkStateText(value) {
  return value === "connected" ? "已连接" : "未连接";
}

function coordinatorStateText(value) {
  return {
    serving: "本机运行中",
    connected: "已连接",
    connecting: "重连中",
    reconnecting: "重连中",
    disconnected: "已断开",
  }[String(value || "").trim().toLowerCase()] || "已断开";
}

const p2pPathLabels = {
  lan_direct: "局域网直连",
  public_direct: "公网直连",
};

function deviceConnectionSummary(device, { officialCloud = false } = {}) {
  const pathState = String(device?.path_state || "").trim().toLowerCase();
  const pathType = String(device?.path_type || "").trim().toLowerCase();
  const status = String(device?.status || "").trim().toLowerCase();
  const kind = String(device?.kind || "").trim().toLowerCase();
  const errorCode = String(device?.error_code || device?.last_error || "").trim().toLowerCase();
  if (kind === "self") {
    return { label: status === "online" ? "本机在线" : "本机离线", tone: status === "online" ? "direct" : "offline" };
  }
  if (status === "disabled") return { label: "已禁用", tone: "failed" };
  if (status === "revoked") return { label: "已吊销", tone: "failed" };
  if (pathState === "rdp-unreachable") return { label: "RDP 不可达", tone: "warning" };
  if (pathState === "waiting_coordinator") return { label: "等待协调服务器", tone: "warning" };
  if (["connecting", "requesting", "preparing", "punching", "authenticating", "reconnecting", "trying_lan_direct", "trying_public_direct"].includes(pathState)) {
    return { label: "正在协商", tone: "warning" };
  }
  if (pathState === "failed" || (!officialCloud && pathState === "fallback_relay")) {
    return {
      label: officialCloud ? "连接失败"
        : errorCode === "direct_unreachable_no_relay" || pathState === "fallback_relay"
          ? "直连失败 · 本版本未启用中继"
          : "直连失败",
      tone: "failed",
    };
  }
  if (officialCloud && (pathType === "relay" || pathState === "fallback_relay")) return { label: "中继", tone: "relay" };
  const directState = pathState === "lan_direct_connected" ? "lan_direct"
    : pathState === "public_direct_connected" ? "public_direct"
      : pathState;
  const directPath = p2pPathLabels[directState] ? directState : pathType;
  if (p2pPathLabels[directPath]) return { label: p2pPathLabels[directPath], tone: "direct" };
  if (status === "online" && (!pathState || pathState === "idle")) {
    return { label: "已在线 · 尚未建立直连", tone: "warning" };
  }
  if (pathState === "closed" || pathState === "offline" || pathState === "offline_or_unknown" || pathState === "idle" || status === "offline") {
    return { label: "离线或未知", tone: "offline" };
  }
  return { label: status === "online" ? "离线或未知" : deviceStatusText(status, kind), tone: "offline" };
}

function connectionSummaryParts(device, coordinatorState = "") {
  const parts = [];
  const latency = Number(device?.latency_ms || 0);
  if (Number.isFinite(latency) && latency > 0) parts.push(`${Math.round(latency)} ms`);
  const score = Number(device?.quality_score || 0);
  if (Number.isFinite(score) && score > 0) parts.push(`质量 ${Math.round(score)}`);
  const pathType = String(device?.path_type || "").trim().toLowerCase();
  if (coordinatorState && coordinatorState !== "connected" && (pathType === "lan_direct" || pathType === "public_direct")) {
    parts.push("协调服务器离线，当前直连不受影响");
  }
  return parts;
}

function deviceConnectionText(device) {
  const summary = deviceConnectionSummary(device);
  return [summary.label, ...connectionSummaryParts(device, state.coordinatorState)].join(" · ");
}

async function loadInfo() {
  const [info, defaults] = await Promise.all([api("/api/info"), api("/api/onboarding/defaults")]);
  state.cwd = info.cwd;
  state.officialHubEnabled = Boolean(info.features?.official_hub_mvp);
  document.querySelectorAll("[data-official-hub]").forEach((element) => {
    element.hidden = !state.officialHubEnabled;
  });
  if ($("configPath")) $("configPath").value = defaults.config_path;
  $("listenPort").value = String(defaults.listen_port || 8443);
  if ($("officialDeviceName") && !$("officialDeviceName").value) $("officialDeviceName").value = defaults.node_name || "";
  const certOut = $("certOut");
  if (certOut) certOut.value = `${info.cwd}\\certs`;
}

async function refreshStatus() {
  const name = encodeURIComponent($("serviceName").value);
  const data = await api(`/api/service/status?service_name=${name}`);
  const status = data.status;
  const label = $("serviceState");
  const text = serviceStateText(status);
  label.textContent = text;
  const copy = $("serviceStatusCopy");
  if (copy) copy.textContent = text;
  label.className = "status";
  if (status.state === "running") label.classList.add("running");
  if (status.state === "stopped" || !status.installed) label.classList.add("stopped");
}

async function serviceAction(action, button) {
  setBusy(button, true);
  try {
    await api("/api/service", {
      method: "POST",
      body: JSON.stringify({
        action,
        service_name: $("serviceName").value,
        config_path: $("configPath").value,
      }),
    });
    toast(`服务${actionText(action)}完成`);
    await refreshStatus();
  } catch (err) {
    toast(err.message, true);
    throw err;
  } finally {
    setBusy(button, false);
  }
}

async function installAndStartAgent(configPath) {
  $("configPath").value = configPath;
  try {
    const name = encodeURIComponent($("serviceName").value);
    const data = await api(`/api/service/status?service_name=${name}`);
    if (data.status?.installed && data.status.state && data.status.state !== "stopped") {
      await api("/api/service", {
        method: "POST",
        body: JSON.stringify({
          action: "stop",
          service_name: $("serviceName").value,
          config_path: configPath,
        }),
      });
    }
  } catch {
    // Best effort: install/start below will surface the actionable error.
  }
  const startedAt = Date.now();
  try {
    await api("/api/service", {
      method: "POST",
      body: JSON.stringify({
        action: "install",
        service_name: $("serviceName").value,
        config_path: configPath,
      }),
    });
  } catch (err) {
    if (!String(err.message).includes("already exists")) throw err;
  }
  try {
    await api("/api/service", {
      method: "POST",
      body: JSON.stringify({
        action: "start",
        service_name: $("serviceName").value,
        config_path: configPath,
      }),
    });
  } catch (err) {
    if (!String(err.message).toLowerCase().includes("already")) throw err;
  }
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    const params = new URLSearchParams({ service_name: $("serviceName").value });
    const data = await api(`/api/onboarding/devices?${params}`);
    const devices = data.devices || {};
    if (Date.parse(devices.updated_at) >= startedAt && devices.network_state === "connected" && ["connected", "serving"].includes(devices.coordinator_state)) {
      await refreshStatus();
      return;
    }
    await new Promise((resolve) => window.setTimeout(resolve, 300));
  }
  throw new Error("未连接服务器，请检查接入地址与服务器运行状态。");
}

async function startServer(button) {
  setBusy(button, true);
  $("serverStatus").textContent = "正在准备";
  let stoppedPrevious = false;
  let configPrepared = false;
  try {
    if (!$("serverAddress").value.trim()) throw new Error("请填写其他设备能够访问的域名或公网地址");
    const name = encodeURIComponent($("serviceName").value);
    const previous = await api(`/api/service/status?service_name=${name}`);
    if (previous.status?.installed && previous.status.state !== "stopped") {
      await api("/api/service", {
        method: "POST",
        body: JSON.stringify({ action: "stop", service_name: $("serviceName").value }),
      });
      stoppedPrevious = true;
    }
    const data = await api("/api/onboarding/start-server", {
      method: "POST",
      body: JSON.stringify({
        server_address: $("serverAddress").value.trim(),
        listen_port: Number($("listenPort").value || 8443),
        long_lived: true,
        max_uses: inviteMaxUses(),
      }),
    });
    configPrepared = true;
    const result = data.result;
    $("configPath").value = result.config_path;
    $("serverStatus").textContent = "配置已生成";
    $("localVirtualIP").textContent = result.virtual_ip || "-";
    $("serverListen").textContent = result.listen || "-";
    renderInvite(result.invite);
    if (result.invite?.server) $("serverAddress").value = result.invite.server;
    try {
      await installAndStartAgent(result.config_path);
      $("serverStatus").textContent = "服务器运行中";
      toast("服务器已启动，接入码已生成");
    } catch (err) {
      $("serverStatus").textContent = "服务启动失败";
      toast(`接入信息已生成，服务启动失败：${err.message}`, true);
    }
    await refreshDevices();
  } catch (err) {
    if (stoppedPrevious && !configPrepared) {
      try {
        await api("/api/service", {
          method: "POST",
          body: JSON.stringify({ action: "start", service_name: $("serviceName").value }),
        });
      } catch (restartError) {
        err = new Error(`${err.message}；原服务恢复失败：${restartError.message}`);
      }
    }
    $("serverStatus").textContent = "启动失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function stopServer(button) {
  try {
    await serviceAction("stop", button);
    $("serverStatus").textContent = "已停止";
    await refreshDevices();
  } catch {
    $("serverStatus").textContent = "停止失败";
  }
}

async function regenerateInvite(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/onboarding/invite", {
      method: "POST",
      body: JSON.stringify({
        server: serverForInvite(),
        protocol: "tcp_tls_v1",
        long_lived: true,
        max_uses: inviteMaxUses(),
        replace_existing: true,
      }),
    });
    renderInvite(data.invite);
    toast("接入码已更新，已连接的设备继续使用原身份");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function serverForInvite() {
  const raw = $("serverAddress").value.trim();
  if (!raw) throw new Error("请填写域名或公网地址");
  if (raw.includes("://")) return raw;
  if (/:\d+$/.test(raw)) return raw;
  return `${raw}:${Number($("listenPort").value || 8443)}`;
}

function renderInvite(invite) {
  state.invite = invite;
  $("accessLink").value = invite?.link || "";
  $("accessCode").textContent = invite?.code || "------";
  updateInviteExpiry();
  window.clearInterval(state.inviteTimer);
  state.inviteTimer = window.setInterval(updateInviteExpiry, 1000);
}

function updateInviteExpiry() {
  const invite = state.invite;
  const label = $("inviteExpiry");
  if (!invite) {
    label.textContent = "服务器启动后生成接入码";
    return;
  }
  if (invite.long_lived) {
    if (invite.max_uses === -1) {
      label.textContent = "长期有效，可供多台设备连接";
      return;
    }
    const maxUses = invite.max_uses || inviteMaxUses();
    label.textContent = `长期有效，设备数限制 ${maxUses} 台。长期接入码风险较高，请妥善保管。`;
    return;
  }
  const expires = new Date(invite.expires_at);
  if (Number.isNaN(expires.getTime())) {
    label.textContent = "10 分钟有效，使用一次失效";
    return;
  }
  const remaining = Math.max(0, expires.getTime() - Date.now());
  const minutes = Math.floor(remaining / 60000);
  const seconds = Math.floor((remaining % 60000) / 1000);
  label.textContent = `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")} 后过期，使用一次失效`;
}

function inviteMaxUses() {
  const input = $("inviteMaxUses");
  if (!input) return -1;
  const value = Number(input?.value || 0);
  if (!Number.isFinite(value) || value <= 0) return 3;
  return Math.floor(value);
}

async function loadOfficialHubState() {
  const data = await api("/api/official-hub/state");
  renderOfficialHubState(data.state || {});
  if (data.state?.organization_id) await officialRefreshTeam(null, true);
}

async function loadSubscriptionExperience() {
  const params = new URLSearchParams();
  const hubURL = $("officialHubAPIURL")?.value?.trim();
  if (hubURL) params.set("hub_api_url", hubURL);
  const data = await api(`/api/official-hub/subscription-experience?${params}`);
  renderSubscriptionExperience(data.experience || {});
}

function officialHubBaseRequest() {
  return {
    hub_api_url: $("officialHubAPIURL").value.trim(),
  };
}

async function officialCreateAccount(button) {
  setBusy(button, true);
  $("officialHubStatus").textContent = "正在创建测试账号";
  try {
    const data = await api("/api/official-hub/account", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        email: $("officialAccountEmail").value.trim(),
        display_name: $("officialAccountName").value.trim(),
      }),
    });
    renderOfficialHubState(data.state || {});
    await loadSubscriptionExperience().catch(() => {});
    $("officialHubStatus").textContent = "测试账号已创建";
    toast("官方 Hub 测试账号已创建");
  } catch (err) {
    $("officialHubStatus").textContent = "账号创建失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function officialCreateNetwork(button) {
  setBusy(button, true);
  $("officialHubStatus").textContent = "正在创建官方网络";
  try {
    const data = await api("/api/official-hub/network", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        name: $("officialNetworkName").value.trim(),
      }),
    });
    renderOfficialHubState(data.state || {});
    await loadSubscriptionExperience().catch(() => {});
    $("officialHubStatus").textContent = "官方网络已创建";
    toast("官方网络已创建");
  } catch (err) {
    $("officialHubStatus").textContent = "网络创建失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function officialCreateInvite(button) {
  setBusy(button, true);
  $("officialHubStatus").textContent = "正在生成官方邀请";
  try {
    const data = await api("/api/official-hub/invite", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        max_uses: 2,
        one_time: false,
      }),
    });
    state.officialInvite = data.invite || null;
    renderOfficialHubState(data.state || {});
    renderOfficialInvite(state.officialInvite);
    await loadSubscriptionExperience().catch(() => {});
    $("officialHubStatus").textContent = "官方邀请已生成";
    toast("官方邀请已生成");
  } catch (err) {
    $("officialHubStatus").textContent = "邀请生成失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function officialJoinDevice(button) {
  const invite = state.officialInvite;
  if (!invite?.token || !invite?.code) {
    toast("请先生成官方邀请", true);
    return;
  }
  setBusy(button, true);
  $("officialHubStatus").textContent = "正在加入当前设备";
  try {
    const data = await api("/api/official-hub/join-device", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        token: invite.token,
        code: invite.code,
        device_name: $("officialDeviceName").value.trim(),
      }),
    });
    renderOfficialHubState(data.state || {});
    $("officialHubStatus").textContent = "当前设备已加入";
    toast("当前设备已加入官方网络");
    await officialRefreshDevices($("officialRefreshDevices"));
  } catch (err) {
    $("officialHubStatus").textContent = "设备加入失败";
    if (err.quota) renderQuotaIssue(err.quota, $("quotaIssueList"));
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function officialHeartbeat(button) {
  setBusy(button, true);
  $("officialHubStatus").textContent = "正在发送 heartbeat";
  try {
    const data = await api("/api/official-hub/heartbeat", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        status: "online",
      }),
    });
    renderOfficialHubState(data.state || {});
    $("officialHubStatus").textContent = "heartbeat 已发送";
    toast("heartbeat 已发送");
    await officialRefreshDevices($("officialRefreshDevices"));
  } catch (err) {
    $("officialHubStatus").textContent = "heartbeat 失败";
    if (err.quota) renderQuotaIssue(err.quota, $("quotaIssueList"));
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function officialRefreshDevices(button) {
  setBusy(button, true);
  try {
    const params = new URLSearchParams();
    const hubURL = $("officialHubAPIURL").value.trim();
    if (hubURL) params.set("hub_api_url", hubURL);
    const data = await api(`/api/official-hub/devices?${params}`);
    renderOfficialHubState(data.state || {});
    renderRelayUsageReminder(data.relay_usage_reminder || null, $("officialRelayReminder"));
    renderOfficialDevices(data.devices || []);
    await loadSubscriptionExperience().catch(() => {});
    $("officialHubStatus").textContent = "官方设备列表已刷新";
  } catch (err) {
    $("officialHubStatus").textContent = "设备列表刷新失败";
    if (err.quota) renderQuotaIssue(err.quota, $("quotaIssueList"));
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function officialTeamErrorText(err) {
  const message = String(err?.message || err || "操作失败");
  const lower = message.toLowerCase();
  if (lower.includes("organization is suspended") || lower.includes("组织已暂停")) return `组织已暂停：${message}`;
  if (lower.includes("quota") || lower.includes("额度")) return `额度不足：${message}`;
  if (lower.includes("private license") || lower.includes("授权已到期") || lower.includes("license policy")) return `私有部署授权拒绝：${message}`;
  if (lower.includes("membership is not active")) return `成员状态不可用：${message}`;
  if (lower.includes("forbidden") || lower.includes("lacks") || lower.includes("cannot")) return `权限不足：${message}`;
  return message;
}

function setOfficialTeamMessage(message, error = false) {
  const el = $("officialTeamMessage");
  if (!el) return;
  el.textContent = message;
  el.className = error ? "team-message error" : "team-message";
}

async function officialCreateOrganization(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/official-hub/organization", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), name: $("officialOrganizationName").value.trim() }),
    });
    renderOfficialHubState(data.state || {});
    setOfficialTeamMessage("组织已创建，可继续管理成员、分组和连接授权。");
    await officialRefreshTeam(null, true);
  } catch (err) {
    setOfficialTeamMessage(officialTeamErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

async function officialRefreshTeam(button, quiet = false) {
  setBusy(button, true);
  try {
    const params = new URLSearchParams();
    const hubURL = $("officialHubAPIURL")?.value?.trim();
    if (hubURL) params.set("hub_api_url", hubURL);
    const data = await api(`/api/official-hub/team?${params}`);
    renderOfficialTeam(data);
    renderOfficialHubState(data.state || {});
    await officialRefreshDeployments(null, true);
    await officialQueryAudit(null, "reset", true);
    if (!quiet) setOfficialTeamMessage("团队数据已刷新。");
  } catch (err) {
    setOfficialTeamMessage(officialTeamErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function officialDeploymentErrorText(err) {
  const message = officialTeamErrorText(err);
  const lower = String(message).toLowerCase();
  if (lower.includes("trusted update catalog")) return `目标版本不受支持：${message}`;
  if (lower.includes("binding mismatch")) return `引导凭据绑定不匹配：${message}`;
  if (lower.includes("expired")) return `引导凭据已过期：${message}`;
  if (lower.includes("revoked")) return `引导凭据已撤销：${message}`;
  if (lower.includes("use limit")) return `引导凭据已达到使用上限：${message}`;
  return message;
}

function privateLicenseStateText(value) {
  return { active: "有效", grace_period: "宽限期", expired: "已到期" }[value] || value || "未知";
}

function renderOfficialPrivateLicense(license) {
  const summary = $("officialPrivateLicenseSummary");
  if (!summary) return;
  const entitlements = license?.entitlements || {};
  const support = license?.support || {};
  summary.textContent = [
    `授权 ID：${license?.license_id || "-"}`,
    `Key ID：${license?.key_id || "-"}`,
    `Organization：${license?.organization_id || "-"}`,
    `Deployment：${license?.deployment_id || "-"}`,
    `状态：${privateLicenseStateText(license?.state)}；到期策略：${license?.expiry_policy || "-"}`,
    `生效：${license?.not_before || "-"}；到期：${license?.expires_at || "-"}${license?.grace_ends_at ? `；宽限结束：${license.grace_ends_at}` : ""}`,
    `设备 ${entitlements.device_count || 0}；成员 ${entitlements.member_count || 0}；并发 ${entitlements.concurrent_online_devices || 0}；审计 ${entitlements.audit_retention_days || 0} 天；部署 ${entitlements.deployment_count || 0}`,
    `Relay：${entitlements.relay ? "允许" : "不允许"}；rollout：${entitlements.rollout ? "允许" : "不允许"}；离线更新：${entitlements.offline_updates ? "允许" : "不允许"}`,
    `支持标识：${support.id || "-"}；联系方式：${support.contact || "-"}`,
  ].join("\n");
}

async function officialRefreshPrivateLicense(button) {
  setBusy(button, true);
  try {
    const query = officialDeploymentQuery();
    const data = await api(`/api/official-hub/team/private-license?${query}`);
    renderOfficialPrivateLicense(data.license || {});
    $("officialPrivateLicenseMessage").className = "team-message";
    $("officialPrivateLicenseMessage").textContent = "授权摘要已刷新；页面未接收原始签名或密钥材料。";
  } catch (err) {
    $("officialPrivateLicenseMessage").textContent = officialTeamErrorText(err);
    $("officialPrivateLicenseMessage").className = "team-message error";
  } finally {
    setBusy(button, false);
  }
}

async function officialImportPrivateLicense(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/official-hub/team/private-license", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), signed_license: $("officialPrivateLicenseDocument").value }),
    });
    $("officialPrivateLicenseDocument").value = "";
    renderOfficialPrivateLicense(data.license || {});
    $("officialPrivateLicenseMessage").className = "team-message";
    $("officialPrivateLicenseMessage").textContent = "已验证并原子导入；输入框中的原始授权已清除。";
  } catch (err) {
    $("officialPrivateLicenseMessage").className = "team-message error";
    $("officialPrivateLicenseMessage").textContent = officialTeamErrorText(err);
  } finally {
    setBusy(button, false);
  }
}

function setOfficialDeploymentMessage(message, error = false) {
  const el = $("officialDeploymentMessage");
  if (!el) return;
  el.textContent = message;
  el.className = error ? "team-message error" : "team-message";
}

function officialDeploymentQuery() {
  const params = new URLSearchParams();
  const hubURL = $("officialHubAPIURL")?.value?.trim();
  if (hubURL) params.set("hub_api_url", hubURL);
  return params;
}

async function officialCreateDeploymentBundle(button) {
  setBusy(button, true);
  try {
    const ttlMinutes = Number($("officialDeploymentTTLMinutes").value || 15);
    const data = await api("/api/official-hub/team/deployment-bundle", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(),
        group_id: $("officialDeploymentGroupID").value.trim(),
        platform: $("officialDeploymentPlatform").value,
        architecture: $("officialDeploymentArchitecture").value,
        ttl_seconds: ttlMinutes * 60,
        max_uses: Number($("officialDeploymentMaxUses").value || 1),
      }),
    });
    const result = data.result || {};
    $("officialDeploymentCredential").textContent = result.credential || "生成响应未返回凭据";
    renderOfficialDeploymentArtifacts(result.files || []);
    $("officialDeploymentCredentialID").value = result.bundle?.credential_id || "";
    setOfficialDeploymentMessage("部署包已生成。请立即安全交付一次显示的凭据；刷新后不会再次显示。", false);
    await officialRefreshDeployments(null, true, true);
  } catch (err) {
    $("officialDeploymentCredential").textContent = "未生成";
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

async function officialRevokeDeploymentCredential(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/official-hub/team/bootstrap-credential/revoke", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), credential_id: $("officialDeploymentCredentialID").value.trim() }),
    });
    setOfficialDeploymentMessage(`凭据 ${data.credential?.id || ""} 已撤销。`, false);
    $("officialDeploymentCredential").textContent = "已撤销；明文不会再次显示";
    renderOfficialDeploymentArtifacts([]);
    await officialRefreshDeployments(null, true);
  } catch (err) {
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

async function officialRefreshDeployments(button, quiet = false, preserveOneTime = false) {
  setBusy(button, true);
  if (!preserveOneTime) {
    $("officialDeploymentCredential").textContent = "一次性凭据已从当前界面清除";
    renderOfficialDeploymentArtifacts([]);
  }
  try {
    const query = officialDeploymentQuery();
    const [bundleData, rolloutData] = await Promise.all([
      api(`/api/official-hub/team/deployment-bundles?${query}`),
      api(`/api/official-hub/team/rollouts?${query}`),
    ]);
    renderOfficialDeploymentBundles(bundleData.bundles || []);
    renderOfficialRollouts(rolloutData.rollouts || []);
    if (!quiet) setOfficialDeploymentMessage("部署包与 rollout 状态已刷新。", false);
  } catch (err) {
    renderOfficialDeploymentBundles([]);
    renderOfficialRollouts([]);
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function renderOfficialDeploymentBundles(bundles) {
  renderOfficialTeamRows($("officialDeploymentBundleList"), bundles, (bundle) => {
    const credential = bundle.credential || {};
    const row = officialTeamRow(`${bundle.platform}/${bundle.architecture} · 模板 v${bundle.template_version}`, `${bundle.status} · 凭据 ${credential.status || "-"} ${credential.uses || 0}/${credential.max_uses || 0} · ${bundle.id}`);
    row.addEventListener("click", () => {
      $("officialDeploymentCredentialID").value = bundle.credential_id || "";
      $("officialDeploymentGroupID").value = bundle.group_id || "";
    });
    return row;
  });
}

function renderOfficialDeploymentArtifacts(files) {
  state.officialDeploymentObjectURLs.forEach((url) => URL.revokeObjectURL(url));
  state.officialDeploymentObjectURLs = [];
  const list = $("officialDeploymentArtifacts");
  if (!files.length) {
    list.replaceChildren(emptyState("一次性脚本内容已清除；服务端不会再次返回"));
    return;
  }
  list.replaceChildren(...files.map((file) => {
    const row = document.createElement("article");
    row.className = "team-row";
    const detail = document.createElement("div");
    const name = document.createElement("strong");
    name.textContent = file.name || "artifact";
    const checksum = document.createElement("small");
    checksum.textContent = `SHA-256 ${file.sha256 || "-"} · ${file.size || 0} bytes`;
    detail.append(name, checksum);
    const link = document.createElement("a");
    const url = URL.createObjectURL(new Blob([file.content || ""], { type: "text/plain;charset=utf-8" }));
    state.officialDeploymentObjectURLs.push(url);
    link.href = url;
    link.download = file.name || "meshlink-deployment-artifact.txt";
    link.textContent = "下载";
    link.className = "artifact-download";
    row.append(detail, link);
    return row;
  }));
}

function rolloutDeviceIDs() {
  return $("officialRolloutDeviceIDs").value.split(",").map((value) => value.trim()).filter(Boolean);
}

async function officialCreateRollout(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/official-hub/team/rollout", {
      method: "POST",
      body: JSON.stringify({
        ...officialHubBaseRequest(), group_id: $("officialRolloutGroupID").value.trim(),
        device_ids: rolloutDeviceIDs(), target_version: $("officialRolloutTargetVersion").value.trim(),
      }),
    });
    const result = data.result || {};
    $("officialRolloutID").value = result.rollout?.id || "";
    renderOfficialRolloutTargets(result.targets || [], result.rollout?.id || "");
    setOfficialDeploymentMessage(`Rollout ${result.rollout?.id || ""} 已创建。`, false);
    await officialRefreshDeployments(null, true);
  } catch (err) {
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function renderOfficialRollouts(rollouts) {
  state.officialRollouts = rollouts;
  renderOfficialTeamRows($("officialRolloutList"), rollouts, (rollout) => {
    const row = officialTeamRow(`${rollout.target_version} · ${rollout.status}`, rollout.id || "-");
    row.addEventListener("click", () => {
      $("officialRolloutID").value = rollout.id || "";
      officialViewRollout(null).catch(() => {});
    });
    return row;
  });
}

async function officialViewRollout(button) {
  setBusy(button, true);
  try {
    const params = officialDeploymentQuery();
    params.set("rollout_id", $("officialRolloutID").value.trim());
    const data = await api(`/api/official-hub/team/rollout?${params}`);
    const result = data.result || {};
    renderOfficialRolloutTargets(result.targets || [], result.rollout?.id || "");
    setOfficialDeploymentMessage(`Rollout ${result.rollout?.id || ""}：${result.rollout?.status || "未知"}。`, false);
  } catch (err) {
    renderOfficialRolloutTargets([], "");
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function renderOfficialRolloutTargets(targets, rolloutID) {
  const list = $("officialRolloutTargets");
  if (!targets.length) {
    list.replaceChildren(emptyState("暂无逐设备 rollout 状态"));
    return;
  }
  list.replaceChildren(...targets.map((target) => {
    const row = document.createElement("article");
    row.className = "rollout-target-row";
    const detail = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = `${target.device_id} · ${target.status}`;
    const version = document.createElement("small");
    version.textContent = `当前 ${target.current_version || "-"} → 目标 ${target.target_version || "-"} · 第 ${target.attempt || 1} 次${target.error_code ? ` · ${target.error_code}` : ""}`;
    detail.append(title, version);
    row.append(detail);
    if (target.status === "failed") {
      const retry = document.createElement("button");
      retry.type = "button";
      retry.className = "secondary compact-button";
      retry.textContent = "仅重试此设备";
      retry.addEventListener("click", () => officialRetryRolloutTarget(retry, rolloutID, target.device_id));
      row.append(retry);
    }
    return row;
  }));
}

async function officialRetryRolloutTarget(button, rolloutID, deviceID) {
  setBusy(button, true);
  try {
    await api("/api/official-hub/team/rollout/retry", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), rollout_id: rolloutID, device_id: deviceID }),
    });
    $("officialRolloutID").value = rolloutID;
    await officialViewRollout(null);
  } catch (err) {
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

async function officialCancelRollout(button) {
  setBusy(button, true);
  try {
    const rolloutID = $("officialRolloutID").value.trim();
    const data = await api("/api/official-hub/team/rollout/cancel", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), rollout_id: rolloutID }),
    });
    renderOfficialRolloutTargets(data.result?.targets || [], rolloutID);
    setOfficialDeploymentMessage("已取消尚未开始的设备；进行中、成功和失败状态保持不变。", false);
    await officialRefreshDeployments(null, true);
  } catch (err) {
    setOfficialDeploymentMessage(officialDeploymentErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

async function officialTeamPost(button, path, payload, successMessage) {
  setBusy(button, true);
  try {
    const data = await api(path, {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), ...payload }),
    });
    setOfficialTeamMessage(successMessage);
    await officialRefreshTeam(null, true);
    return data;
  } catch (err) {
    setOfficialTeamMessage(officialTeamErrorText(err), true);
    return null;
  } finally {
    setBusy(button, false);
  }
}

function officialChangeMemberRole(button) {
  return officialTeamPost(button, "/api/official-hub/team/member-role", {
    account_id: $("officialMemberAccountID").value.trim(),
    role: $("officialMemberRole").value,
  }, "成员角色已更新。");
}

async function officialCreateGroup(button) {
  const data = await officialTeamPost(button, "/api/official-hub/team/group", {
    name: $("officialGroupName").value.trim(),
  }, "设备分组已创建。");
  if (data?.group?.id) {
    $("officialGroupID").value = data.group.id;
    $("officialTeamDeviceGroupID").value = data.group.id;
  }
}

function officialRenameGroup(button) {
  return officialTeamPost(button, "/api/official-hub/team/group/rename", {
    group_id: $("officialGroupID").value.trim(),
    name: $("officialGroupName").value.trim(),
  }, "设备分组已重命名。");
}

function officialDeleteGroup(button) {
  return officialTeamPost(button, "/api/official-hub/team/group/delete", {
    group_id: $("officialGroupID").value.trim(),
  }, "设备分组已删除，相关分组授权已清理。");
}

function officialEnrollTeamDevice(button) {
  const deviceID = $("officialTeamDeviceID").value.trim() || $("officialDeviceID").textContent.trim();
  return officialTeamPost(button, "/api/official-hub/team/device/enroll", { device_id: deviceID }, "设备已纳入组织。");
}

function officialChangeTeamDeviceGroup(button, action) {
  return officialTeamPost(button, "/api/official-hub/team/device/group", {
    device_id: $("officialTeamDeviceID").value.trim(),
    group_id: $("officialTeamDeviceGroupID").value.trim(),
    action,
  }, action === "remove" ? "设备已移出分组。" : "设备已加入分组。");
}

function officialGrantConnection(button) {
  const scope = $("officialGrantScope").value;
  const payload = { member_account_id: $("officialGrantMemberID").value.trim() };
  payload[scope === "group" ? "group_id" : "device_id"] = $("officialGrantTargetID").value.trim();
  return officialTeamPost(button, "/api/official-hub/team/grant", payload, "connect 授权已生效。");
}

function officialRevokeGrant(button, grantID) {
  return officialTeamPost(button, "/api/official-hub/team/grant/revoke", { grant_id: grantID }, "connect 授权已撤销，新协商将立即拒绝。");
}

function setOfficialAuditMessage(message, error = false) {
  const el = $("officialAuditMessage");
  if (!el) return;
  el.textContent = message;
  el.className = error ? "team-message error" : "team-message";
}

function officialAuditErrorText(err) {
  if (err?.quota?.dimension === "audit_log_retention") {
    return `审计保留期不可用：${String(err.message || "请确认套餐或合同配置")}`;
  }
  const message = officialTeamErrorText(err);
  if (String(message).toLowerCase().includes("audit log retention")) return `审计保留期不可用：${message}`;
  return message;
}

function officialAuditTimeValue(id) {
  const raw = $(id)?.value?.trim();
  if (!raw) return "";
  const value = new Date(raw);
  return Number.isNaN(value.getTime()) ? "" : value.toISOString();
}

function officialAuditParams(cursor = "") {
  const params = new URLSearchParams();
  const hubURL = $("officialHubAPIURL")?.value?.trim();
  if (hubURL) params.set("hub_api_url", hubURL);
  const filters = {
    member_account_id: $("officialAuditMemberID")?.value?.trim(),
    source_device_id: $("officialAuditSourceDeviceID")?.value?.trim(),
    target_device_id: $("officialAuditTargetDeviceID")?.value?.trim(),
    start_time: officialAuditTimeValue("officialAuditStartTime"),
    end_time: officialAuditTimeValue("officialAuditEndTime"),
    connection_method: $("officialAuditConnectionMethod")?.value,
    min_relay_bytes: $("officialAuditMinRelayBytes")?.value,
    page_size: $("officialAuditPageSize")?.value,
  };
  Object.entries(filters).forEach(([key, value]) => {
    if (value !== undefined && value !== null && String(value).trim() !== "") params.set(key, String(value).trim());
  });
  if ($("officialAuditRelayOnly")?.checked) params.set("relay_only", "true");
  if (cursor) params.set("cursor", cursor);
  return params;
}

async function officialQueryAudit(button, direction = "reset", quiet = false) {
  setBusy(button, true);
  const previousCursor = state.officialAuditCursor;
  const previousHistory = [...state.officialAuditHistory];
  let cursor = previousCursor;
  if (direction === "reset") {
    cursor = "";
    state.officialAuditHistory = [];
  } else if (direction === "next") {
    if (!state.officialAuditNextCursor) {
      setBusy(button, false);
      return;
    }
    state.officialAuditHistory.push(previousCursor);
    cursor = state.officialAuditNextCursor;
  } else if (direction === "previous") {
    cursor = state.officialAuditHistory.pop() || "";
  }
  try {
    const data = await api(`/api/official-hub/team/audit?${officialAuditParams(cursor)}`);
    state.officialAuditCursor = cursor;
    state.officialAuditNextCursor = data.page?.next_cursor || "";
    renderOfficialAuditPage(data.page || {});
    if (!quiet) setOfficialAuditMessage("组织审计已刷新。", false);
  } catch (err) {
    state.officialAuditCursor = previousCursor;
    state.officialAuditHistory = previousHistory;
    state.officialAuditNextCursor = "";
    renderOfficialAuditPage({ entries: [] });
    setOfficialAuditMessage(officialAuditErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function renderOfficialAuditPage(page) {
  const entries = page.entries || [];
  const list = $("officialAuditList");
  if (list) {
    if (!entries.length) {
      list.replaceChildren(emptyState("暂无符合条件的审计记录"));
    } else {
      list.replaceChildren(...entries.map(officialAuditRow));
    }
  }
  renderOfficialAuditRetention(page.retention || null);
  if ($("officialAuditPrevious")) $("officialAuditPrevious").disabled = state.officialAuditHistory.length === 0;
  if ($("officialAuditNext")) $("officialAuditNext").disabled = !state.officialAuditNextCursor;
}

function renderOfficialAuditRetention(retention) {
  const el = $("officialAuditRetention");
  if (!el) return;
  if (!retention) {
    el.textContent = "尚未解析审计保留期。";
    return;
  }
  const policy = retention.mode === "unlimited" ? "套餐保留期：不限" : `套餐保留期：${retention.retention_days || 0} 天`;
  const effective = `有效范围：${formatTime(retention.effective_start)} 至 ${formatTime(retention.effective_end)}`;
  const truncated = retention.truncated ? "；请求起点早于保留期，结果已按套餐截断" : "";
  const rangeEmpty = retention.range_empty ? "；请求区间整体早于保留期，本页为空并非没有历史日志" : "";
  el.textContent = `${policy}；${effective}${truncated}${rangeEmpty}。`;
}

function officialAuditRow(entry) {
  const row = document.createElement("article");
  row.className = "audit-row";
  const when = document.createElement("div");
  const timeText = document.createElement("strong");
  timeText.textContent = formatTime(entry.time);
  const kind = document.createElement("small");
  kind.textContent = entry.kind === "connection" ? "连接日志" : "组织事件";
  when.append(timeText, kind);
  const detail = document.createElement("div");
  const action = document.createElement("strong");
  action.textContent = `${entry.action || "-"} · ${entry.result || "-"}`;
  const identity = document.createElement("p");
  identity.textContent = `actor ${entry.actor_account_id || "-"} · member ${entry.member_account_id || "-"}`;
  const devices = document.createElement("p");
  devices.textContent = `源 ${entry.source_device_id || "-"} → 目标 ${entry.target_device_id || "-"}`;
  const deployment = document.createElement("p");
  deployment.textContent = `部署包 ${entry.bundle_id || "-"} · rollout ${entry.rollout_id || "-"} · 分组 ${entry.group_id || "-"} · 版本 ${entry.current_version || "-"} → ${entry.target_version || "-"}`;
  const connection = document.createElement("p");
  const relayBytes = Number(entry.relay_bytes_in || 0) + Number(entry.relay_bytes_out || 0);
  connection.textContent = `方式 ${entry.connection_method || "-"} · 权限 ${entry.permission_source || "-"} · Relay ${relayByteText(relayBytes)}`;
  detail.append(action, identity, devices, deployment, connection);
  row.append(when, detail);
  return row;
}

async function officialCleanupAudit(button) {
  const confirmed = window.confirm("确认按组织 owner 当前套餐保留期清理过期审计和连接日志？此操作应先完成备份，且不能撤销。");
  if (!confirmed) return;
  setBusy(button, true);
  try {
    const batchSize = Number($("officialAuditCleanupBatchSize")?.value || 100);
    const data = await api("/api/official-hub/team/audit/cleanup", {
      method: "POST",
      body: JSON.stringify({ ...officialHubBaseRequest(), batch_size: batchSize }),
    });
    const result = data.result || {};
    $("officialAuditCleanupResult").textContent = `已清理 ${result.deleted_total || 0} 条（组织事件 ${result.deleted_audit_events || 0}、连接日志 ${result.deleted_connection_logs || 0}）${result.has_more ? "，仍有下一批" : "，当前批次已完成"}。`;
    setOfficialAuditMessage("审计清理已完成，并已记录清理结果。", false);
    await officialQueryAudit(null, "reset", true);
  } catch (err) {
    $("officialAuditCleanupResult").textContent = officialAuditErrorText(err);
    setOfficialAuditMessage(officialAuditErrorText(err), true);
  } finally {
    setBusy(button, false);
  }
}

function renderOfficialTeam(team) {
  state.officialTeam = team;
  const organization = team.organization || {};
  $("officialOrganizationID").textContent = organization.id || "-";
  $("officialOrganizationStatus").textContent = organization.status || "-";
  if (organization.name) $("officialOrganizationName").value = organization.name;
  renderOfficialTeamRows($("officialTeamMembers"), team.members || [], (member) => {
    const row = officialTeamRow(`${member.account_id} · ${member.role}`, member.status || "-");
    row.addEventListener("click", () => {
      $("officialMemberAccountID").value = member.account_id || "";
      $("officialGrantMemberID").value = member.account_id || "";
      if (["admin", "operator", "member"].includes(member.role)) $("officialMemberRole").value = member.role;
    });
    return row;
  });
  renderOfficialTeamRows($("officialTeamGroups"), team.groups || [], (group) => {
    const row = officialTeamRow(group.name || group.id, group.id || "-");
    row.addEventListener("click", () => {
      $("officialGroupID").value = group.id || "";
      $("officialGroupName").value = group.name || "";
      $("officialTeamDeviceGroupID").value = group.id || "";
      $("officialGrantScope").value = "group";
      $("officialGrantTargetID").value = group.id || "";
    });
    return row;
  });
  renderOfficialTeamRows($("officialTeamDevices"), team.devices || [], (device) => {
    const row = officialTeamRow(device.device_id || device.id, device.group_id ? `分组 ${device.group_id}` : "未分组");
    row.addEventListener("click", () => {
      $("officialTeamDeviceID").value = device.device_id || "";
      $("officialTeamDeviceGroupID").value = device.group_id || "";
      $("officialGrantScope").value = "device";
      $("officialGrantTargetID").value = device.device_id || "";
    });
    return row;
  });
  renderOfficialTeamRows($("officialTeamGrants"), team.grants || [], (grant) => {
    const target = grant.scope === "group" ? grant.group_id : grant.device_id;
    const row = officialTeamRow(`${grant.member_account_id} → ${target}`, grant.scope || "-");
    const revoke = document.createElement("button");
    revoke.type = "button";
    revoke.className = "danger compact-button";
    revoke.textContent = "撤销";
    revoke.addEventListener("click", (event) => {
      event.stopPropagation();
      officialRevokeGrant(revoke, grant.id);
    });
    row.append(revoke);
    return row;
  });
}

function renderOfficialTeamRows(container, items, render) {
  if (!container) return;
  if (!items.length) {
    container.replaceChildren(emptyState("暂无数据"));
    return;
  }
  container.replaceChildren(...items.map(render));
}

function officialTeamRow(titleText, detailText) {
  const row = document.createElement("article");
  row.className = "team-row";
  const body = document.createElement("div");
  const title = document.createElement("strong");
  title.textContent = titleText || "-";
  const detail = document.createElement("small");
  detail.textContent = detailText || "-";
  body.append(title, detail);
  row.append(body);
  return row;
}

function renderOfficialHubState(hubState) {
  if (!hubState) return;
  state.officialHubState = hubState;
  const url = hubState.hub_api_url || hubState.suggested_hub_api_url || "http://127.0.0.1:18080";
  if ($("officialHubAPIURL") && url) $("officialHubAPIURL").value = url;
  if ($("officialAccountEmail") && hubState.account_email) $("officialAccountEmail").value = hubState.account_email;
  if ($("officialAccountName") && hubState.account_name) $("officialAccountName").value = hubState.account_name;
  if ($("officialNetworkName") && hubState.network_name) $("officialNetworkName").value = hubState.network_name;
  if ($("officialDeviceName") && hubState.local_device_name) $("officialDeviceName").value = hubState.local_device_name;
  $("officialAccountID").textContent = hubState.account_id || "-";
  $("officialNetworkID").textContent = hubState.network_id || "-";
  $("officialDeviceID").textContent = hubState.device_id || "-";
  if ($("officialOrganizationID")) $("officialOrganizationID").textContent = hubState.organization_id || "-";
  if ($("officialOrganizationName") && hubState.organization_name) $("officialOrganizationName").value = hubState.organization_name;
  if ($("officialTeamDeviceID") && hubState.device_id && !$("officialTeamDeviceID").value) $("officialTeamDeviceID").value = hubState.device_id;
  if (hubState.last_invite && !state.officialInvite) {
    $("officialInviteExpiry").textContent = officialInviteSummaryText(hubState.last_invite);
  }
}

function renderOfficialInvite(invite) {
  $("officialInviteCode").textContent = invite?.code || "------";
  $("officialInviteExpiry").textContent = invite ? officialInviteSummaryText(invite) : "生成官方邀请后，可将当前设备加入该官方网络。";
}

function officialInviteSummaryText(invite) {
  const parts = [];
  if (invite.max_uses) parts.push(`最多 ${invite.max_uses} 台设备`);
  if (invite.expires_at) {
    const expires = new Date(invite.expires_at);
    if (!Number.isNaN(expires.getTime()) && expires.getFullYear() > 1) {
      parts.push(`有效期至 ${expires.toLocaleString()}`);
    }
  }
  return parts.length ? parts.join("，") : "官方邀请已生成";
}

const subscriptionStatusLabels = {
  pending: "等待生效",
  active: "已生效",
  past_due: "状态需处理",
  canceled: "已取消",
  expired: "已到期",
};

const planOrder = ["free", "personal", "family", "team", "enterprise"];

function renderSubscriptionExperience(experience) {
  state.subscriptionExperience = experience;
  const plan = experience.current_plan || {};
  const subscription = experience.subscription || {};
  $("currentPlanName").textContent = plan.display_name || plan.id || "Free";
  $("subscriptionStateLabel").textContent =
    subscription.label || subscriptionStatusLabels[subscription.status] || "未配置";
  $("subscriptionStatusMessage").textContent =
    subscription.message || "暂时没有官方 Hub 订阅状态；自建能力可继续使用。";
  $("subscriptionEffectiveUntil").textContent = formatSubscriptionDate(subscription.effective_until);
  $("selfHostedContinuity").textContent =
    experience.self_hosted_continuity || "自建服务器、自建 Relay 和基础设备互联可继续使用。";
  renderQuotaIssues(experience.quota_issues || [], $("quotaIssueList"));
  renderQuotas(experience.quotas || [], $("quotaList"));
  renderPlanComparison(experience.plan_comparison || fallbackPlanComparison(), $("planComparison"));
  const entry = experience.upgrade_entry || {};
  $("viewPlans").textContent = entry.label || "查看套餐/了解升级";
  $("upgradePlaceholder").textContent =
    entry.message || "这里只展示本地套餐差异，不跳转外部页面，也不承诺购买结果。";
  renderRelayUsageReminder(experience.relay_usage_reminder || null, $("officialRelayReminder"));
}

function renderQuotas(quotas, container) {
  if (!container) return;
  if (!quotas.length) {
    container.replaceChildren(emptyState("暂无额度数据"));
    return;
  }
  container.replaceChildren(
    ...quotas.map((quota) => {
      const card = document.createElement("article");
      card.className = `quota-card ${quota.severity || "info"}`;
      const head = document.createElement("div");
      head.className = "quota-head";
      const title = document.createElement("strong");
      title.textContent = quota.label || "额度";
      const badge = document.createElement("span");
      badge.className = `badge ${quotaBadgeTone(quota.severity)}`;
      badge.textContent = quota.mode_label || quota.mode || "需确认";
      head.append(title, badge);
      const meter = document.createElement("div");
      meter.className = "quota-meter";
      const fill = document.createElement("span");
      fill.style.width = `${Math.max(0, Math.min(100, Number(quota.usage_percent || 0)))}%`;
      meter.append(fill);
      const stats = document.createElement("p");
      stats.className = "quota-stats";
      stats.textContent = `已用 ${quota.used_text || quota.used || 0} / 上限 ${quota.limit_text || "-"} / 剩余 ${quota.remaining_text || "-"}`;
      card.append(
        head,
        stats,
        quota.usage_percent ? meter : document.createTextNode(""),
        quotaLine("说明", quota.message),
        quotaLine("影响", quota.impact),
        quotaLine("建议", quota.recommendation),
      );
      return card;
    }),
  );
}

function quotaLine(label, value) {
  const row = document.createElement("p");
  const name = document.createElement("strong");
  name.textContent = `${label}：`;
  row.append(name, document.createTextNode(value || "-"));
  return row;
}

function quotaBadgeTone(severity) {
  if (severity === "fail") return "fail";
  if (severity === "warn") return "warn";
  return "ok";
}

function renderQuotaIssues(issues, container) {
  if (!container) return;
  if (!issues.length) {
    container.replaceChildren();
    return;
  }
  container.replaceChildren(...issues.map((issue) => renderQuotaIssue(issue)));
}

function renderQuotaIssue(issue, container) {
  const card = document.createElement("article");
  card.className = "quota-issue";
  const title = document.createElement("strong");
  title.textContent = `${issue.label || quotaDimensionLabel(issue.dimension)}已达到额度`;
  card.append(title, quotaLine("影响", issue.impact || "对应操作会被限制；这不是普通网络错误。"));
  const advice = Array.isArray(issue.advice) && issue.advice.length
    ? issue.advice
    : ["查看套餐状态，确认当前账号可用权益。", "继续使用自建服务器或自建 Relay。"];
  const list = document.createElement("ul");
  list.replaceChildren(
    ...advice.map((item) => {
      const li = document.createElement("li");
      li.textContent = item;
      return li;
    }),
  );
  card.append(list);
  if (container) container.replaceChildren(card);
  return card;
}

function quotaDimensionLabel(dimension) {
  return {
    device_count: "设备数量",
    concurrent_online_devices: "同时在线设备",
    official_relay_traffic: "官方 Relay 流量",
    active_relay_sessions: "官方 Relay 会话",
    relay_sessions_per_day: "每日 Relay 会话",
  }[dimension] || "额度";
}

function renderPlanComparison(plans, container) {
  if (!container) return;
  const byID = new Map(plans.map((plan) => [plan.id, plan]));
  const ordered = planOrder.map((id) => byID.get(id)).filter(Boolean);
  const rows = (ordered.length ? ordered : plans).map((plan) => {
    const row = document.createElement("tr");
    const selfHosted = [
      plan.self_hosted_server,
      plan.self_hosted_relay,
      plan.basic_device_interconnect,
    ].filter(Boolean).join(" / ");
    for (const value of [
      plan.display_name || plan.id,
      plan.device_count,
      plan.concurrent_online_devices,
      plan.official_relay_traffic,
      selfHosted,
    ]) {
      const cell = document.createElement("td");
      cell.textContent = value || "-";
      row.append(cell);
    }
    return row;
  });
  container.replaceChildren(...rows);
}

function fallbackPlanComparison() {
  return [
    { id: "free", display_name: "Free", device_count: "不限制", concurrent_online_devices: "不限制", official_relay_traffic: "不可用", self_hosted_server: "不限制", self_hosted_relay: "不限制", basic_device_interconnect: "不限制" },
    { id: "personal", display_name: "Personal", device_count: "3 台", concurrent_online_devices: "需配置", official_relay_traffic: "需配置", self_hosted_server: "不限制", self_hosted_relay: "不限制", basic_device_interconnect: "不限制" },
    { id: "family", display_name: "Family", device_count: "10 台", concurrent_online_devices: "需配置", official_relay_traffic: "需配置", self_hosted_server: "不限制", self_hosted_relay: "不限制", basic_device_interconnect: "不限制" },
    { id: "team", display_name: "Team", device_count: "需配置", concurrent_online_devices: "需配置", official_relay_traffic: "需配置", self_hosted_server: "不限制", self_hosted_relay: "不限制", basic_device_interconnect: "不限制" },
    { id: "enterprise", display_name: "Enterprise", device_count: "按合同", concurrent_online_devices: "按合同", official_relay_traffic: "按合同", self_hosted_server: "不限制", self_hosted_relay: "不限制", basic_device_interconnect: "不限制" },
  ];
}

function formatSubscriptionDate(raw) {
  if (!raw) return "-";
  const date = new Date(raw);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) return "-";
  return date.toLocaleDateString();
}

function renderOfficialDevices(devices) {
  const list = $("officialDeviceList");
  if (!devices.length) {
    list.replaceChildren(emptyState("暂无官方设备"));
    return;
  }
  list.replaceChildren(
    ...devices.map((device) => {
      const row = document.createElement("article");
      row.className = "device-row";
      const summary = deviceConnectionSummary(device, { officialCloud: true });
      const dot = document.createElement("span");
      dot.className = `dot ${summary.tone}`;
      const main = document.createElement("div");
      main.className = "device-main";
      const titleLine = document.createElement("div");
      titleLine.className = "device-title-line";
      const title = document.createElement("strong");
      title.textContent = device.name || "未命名设备";
      const badge = document.createElement("span");
      badge.className = `connection-pill ${summary.tone}`;
      badge.textContent = summary.label;
      titleLine.append(title, badge);
      const detail = document.createElement("small");
      const parts = connectionSummaryParts(device);
      if (device.last_seen) parts.push(`最近在线 ${formatTime(device.last_seen)}`);
      detail.textContent = parts.join(" · ");
      main.append(titleLine, detail);
      row.append(dot, main);
      return row;
    }),
  );
}

function renderRelayUsageReminder(reminder, container) {
  if (!container) return;
  if (!reminder) {
    container.hidden = true;
    container.replaceChildren();
    return;
  }
  const titles = {
    approaching: "接近中继流量上限",
    reached: "中继流量已达上限",
    overage: "中继流量明显超额",
  };
  const percent = Number(reminder.usage_percent || 0);
  const used = relayByteText(reminder.used_bytes);
  const limit = relayByteText(reminder.limit_bytes);
  const title = reminder.title || titles[reminder.level] || "中继流量提醒";
  const message =
    reminder.message ||
    `当前连接正在走中继，账号本期已用 ${used} / 上限 ${limit}${percent > 0 ? `（${Math.round(percent)}%）` : ""}。`;
  const impact =
    reminder.impact ||
    "达到上限后新的中继连接可能被限制；这不是普通网络错误。";
  const recommendation =
    reminder.recommendation ||
    "优先尝试直连；如果需要长期中继，请使用自建中继或管理员中继；后续升级入口开放后可在这里处理额度。";
  const actions = Array.isArray(reminder.actions) && reminder.actions.length
    ? reminder.actions
    : ["优先尝试直连", "使用自建中继或管理员中继", "升级入口占位"];

  const head = document.createElement("div");
  head.className = "relay-reminder-head";
  const badge = document.createElement("span");
  badge.className = `badge ${reminder.severity === "fail" ? "fail" : "warn"}`;
  badge.textContent = statusText(reminder.severity === "fail" ? "fail" : "warn");
  const name = document.createElement("strong");
  name.textContent = title;
  head.append(badge, name);

  const actionList = document.createElement("ul");
  actionList.className = "relay-reminder-actions";
  actionList.replaceChildren(
    ...actions.map((action) => {
      const item = document.createElement("li");
      item.textContent = action;
      return item;
    }),
  );

  container.hidden = false;
  container.replaceChildren(
    head,
    relayReminderLine("当前用量", message),
    relayReminderLine("可能影响", impact),
    relayReminderLine("建议处理", recommendation),
    actionList,
  );
}

function relayReminderLine(label, value) {
  const row = document.createElement("p");
  const name = document.createElement("strong");
  name.textContent = `${label}：`;
  row.append(name, document.createTextNode(value || "-"));
  return row;
}

function relayByteText(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) return "-";
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let current = bytes;
  for (const unit of units) {
    current /= 1024;
    if (current < 1024) return `${current.toFixed(1)} ${unit}`;
  }
  return `${(current / 1024).toFixed(1)} PiB`;
}

function formatInviteExpiry(invite) {
  if (!invite?.expires_at) return "-";
  const date = new Date(invite.expires_at);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) return "-";
  return date.toLocaleString();
}

async function joinNetwork(button) {
  setBusy(button, true);
  $("clientStatus").textContent = "正在连接…";
  try {
    const data = await api("/api/onboarding/join", {
      method: "POST",
      body: JSON.stringify({
        invite_link: inviteLinkForJoin(),
        code: $("inviteCode").value,
        node_name: $("joinNodeName").value.trim(),
      }),
    });
    const result = data.result;
    $("configPath").value = result.config_path;
    $("clientStatus").textContent = "配置已生成";
    try {
      await installAndStartAgent(result.config_path);
      $("clientStatus").textContent = "已连接";
      toast("已连接服务器");
    } catch (err) {
      $("clientStatus").textContent = "未连接";
      toast(`连接失败：${err.message}`, true);
    }
    await refreshDevices();
  } catch (err) {
    $("clientStatus").textContent = "未连接";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function inviteLinkForJoin() {
  return $("inviteLink").value.trim();
}

async function disconnectNetwork(button) {
  try {
    await serviceAction("stop", button);
    await refreshDevices();
  } catch {
    $("clientStatus").textContent = "断开失败";
  }
}

async function exitNetwork(button) {
  if (!window.confirm("退出后需要重新使用邀请码才能加入。确定退出网络吗？")) return;
  setBusy(button, true);
  try {
    await api("/api/onboarding/leave", {
      method: "POST",
      body: JSON.stringify({ service_name: $("serviceName").value }),
    });
    state.selectedDeviceKey = "";
    state.selectedDevice = null;
    state.networkState = "not_joined";
    state.coordinatorState = "disconnected";
    state.p2pListen = "";
    state.devices = [];
    renderConnectivityOverview();
    renderDevices([]);
    $("clientStatus").textContent = "未加入";
    await refreshStatus().catch(() => {});
    toast("已退出网络");
  } catch (err) {
    $("clientStatus").textContent = "退出失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function refreshDevices() {
  try {
    const params = new URLSearchParams({ service_name: $("serviceName").value });
    const data = await api(`/api/onboarding/devices?${params}`);
    const devices = data.devices || {};
    state.networkState = devices.network_state || "";
    state.coordinatorState = devices.coordinator_state || coordinatorStateFromNetwork(devices.network_state);
    state.p2pListen = devices.p2p_listen || "";
    state.devices = devices.nodes || [];
    $("clientStatus").textContent = devices.coordinator_state === "serving" ? "服务器运行中" : networkStateText(devices.network_state);
    renderConnectivityOverview();
    renderDevices(state.devices);
  } catch (err) {
    invalidateDeviceSnapshot();
    throw err;
  }
}

function conservativeDeviceSnapshot(devices) {
  return (devices || []).map((device) => {
    const status = String(device?.status || "").trim().toLowerCase();
    return {
      ...device,
      status: status === "disabled" || status === "revoked" ? status : "offline",
      path_type: "",
      path_state: "offline",
      quality_score: 0,
      latency_ms: 0,
      packet_loss_per_mille: 0,
      jitter_ms: 0,
      relay_bytes_in: 0,
      relay_bytes_out: 0,
      last_error: "",
    };
  });
}

function invalidateDeviceSnapshot() {
  state.networkState = "disconnected";
  state.coordinatorState = "disconnected";
  state.p2pListen = "";
  state.devices = conservativeDeviceSnapshot(state.devices);
  $("clientStatus").textContent = networkStateText(state.networkState);
  renderConnectivityOverview();
  renderDevices(state.devices);
}

function coordinatorStateFromNetwork(networkState) {
  if (networkState === "connected") return "connected";
  if (networkState === "connecting" || networkState === "reconnecting") return "reconnecting";
  return "disconnected";
}

function renderConnectivityOverview() {
  const coordinator = $("coordinatorState");
  if (coordinator) coordinator.textContent = coordinatorStateText(state.coordinatorState);
  const listen = $("p2pListen");
  if (listen) listen.textContent = state.p2pListen || "-";
  const selectedPeer = state.selectedDevice?.kind === "peer"
    ? state.selectedDevice
    : state.devices.find((device) => device.kind === "peer");
  const path = $("p2pPathState");
  if (path) path.textContent = selectedPeer ? deviceConnectionText(selectedPeer) : "离线或未知";
}

async function pollStatus() {
  if (state.statusPolling) return;
  state.statusPolling = true;
  try {
    await Promise.allSettled([refreshStatus(), refreshDevices()]);
  } finally {
    state.statusPolling = false;
  }
}

function startStatusPolling() {
  if (state.statusPollTimer) return;
  state.statusPollTimer = window.setInterval(() => {
    pollStatus().catch(() => {});
  }, 2000);
}

function renderDevices(devices) {
  const list = $("deviceList");
  if (!devices.length) {
    state.selectedDeviceKey = "";
    state.selectedDevice = null;
    list.replaceChildren(emptyState("暂无节点"));
    $("deviceDetail").textContent = "服务器或客户端启动后，节点会显示在这里。";
    renderConnectivityOverview();
    if ($("deviceDisplayName")) $("deviceDisplayName").value = "";
    setDeviceAdminEnabled(false);
    return;
  }
  list.replaceChildren(...devices.map((device) => deviceRow(device)));
  const selected = devices.find((device) => deviceKey(device) === state.selectedDeviceKey) || devices[0];
  selectDevice(selected);
}

function deviceRow(device) {
  const row = document.createElement("article");
  row.className = "device-row";
  row.dataset.key = deviceKey(device);
  row.tabIndex = 0;

  const summary = deviceConnectionSummary(device);
  const dot = document.createElement("span");
  dot.className = `dot ${summary.tone}`;

  const main = document.createElement("div");
  main.className = "device-main";
  const titleLine = document.createElement("div");
  titleLine.className = "device-title-line";
  const title = document.createElement("strong");
  const displayName = device.display_name || device.node_id || "未命名节点";
  title.textContent = displayName;
  const badge = document.createElement("span");
  badge.className = `connection-pill ${summary.tone}`;
  badge.textContent = summary.label;
  titleLine.append(title, badge);
  const detail = document.createElement("small");
  const parts = [];
  if (device.virtual_ip) parts.push(device.virtual_ip);
  parts.push(...connectionSummaryParts(device, state.coordinatorState));
  if (device.remote_addr) parts.push(`来源 ${sourceIP(device.remote_addr)}`);
  if (device.last_seen) parts.push(`最近在线 ${formatTime(device.last_seen)}`);
  detail.textContent = parts.join(" · ");
  main.append(titleLine, detail);

  const actions = document.createElement("div");
  actions.className = "row-actions";
  const copy = document.createElement("button");
  copy.type = "button";
  copy.className = "secondary";
  copy.textContent = "复制 IP";
  copy.disabled = !device.virtual_ip;
  copy.addEventListener("click", (event) => {
    event.stopPropagation();
    copyText(device.virtual_ip);
  });

  const rdp = document.createElement("button");
  rdp.type = "button";
  rdp.textContent = "远程桌面";
  rdp.disabled = !device.virtual_ip || device.kind === "self" || device.status === "disabled";
  rdp.addEventListener("click", (event) => {
    event.stopPropagation();
    openRdpTarget(device.virtual_ip);
  });

  const diagnose = document.createElement("button");
  diagnose.type = "button";
  diagnose.className = "secondary";
  diagnose.textContent = "诊断";
  diagnose.disabled = !device.virtual_ip || device.kind === "self" || device.status === "disabled";
  diagnose.addEventListener("click", (event) => {
    event.stopPropagation();
    checkRdpTarget(device);
  });

  actions.append(rdp, copy, diagnose);
  row.append(dot, main, actions);
  row.addEventListener("click", () => selectDevice(device));
  row.addEventListener("keydown", (event) => {
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      selectDevice(device);
    }
  });
  return row;
}

function selectDevice(device) {
  state.selectedDeviceKey = deviceKey(device);
  state.selectedDevice = device;
  document.querySelectorAll(".device-row").forEach((row) => {
    row.classList.toggle("selected", row.dataset.key === state.selectedDeviceKey);
  });
  if (device.virtual_ip) $("rdpTarget").value = device.virtual_ip;
  if ($("deviceDisplayName")) $("deviceDisplayName").value = device.display_name || device.node_id || "";
  setDeviceAdminEnabled(canAdminDevice(device));
  renderConnectivityOverview();
  const detail = $("deviceDetail");
  const lines = [
    `显示名称：${device.display_name || device.node_id || "未命名节点"}`,
    `节点名称：${device.node_id || "未命名节点"}`,
    `虚拟 IP：${device.virtual_ip || "-"}`,
    `连接方式：${deviceConnectionText(device)}`,
    `在线状态：${deviceStatusText(device.status, device.kind)}`,
    `最近在线：${formatTime(device.last_seen)}`,
  ];
  detail.textContent = lines.join("\n");
}

function canAdminDevice(device) {
  return !!device && device.kind === "peer" && !!device.node_id;
}

function setDeviceAdminEnabled(enabled) {
  for (const id of ["deviceDisplayName", "renameDevice", "disableDevice", "removeDevice"]) {
    const el = $(id);
    if (el) el.disabled = !enabled;
  }
}

function deviceKey(device) {
  return `${device.kind || "peer"}:${device.node_id || ""}:${device.virtual_ip || ""}`;
}

function emptyState(text) {
  const el = document.createElement("div");
  el.className = "empty";
  el.textContent = text;
  return el;
}

function sourceIP(remoteAddr) {
  if (!remoteAddr) return "";
  const index = remoteAddr.lastIndexOf(":");
  return index > 0 ? remoteAddr.slice(0, index) : remoteAddr;
}

function formatTime(raw) {
  if (!raw) return "-";
  const date = new Date(raw);
  if (Number.isNaN(date.getTime()) || date.getFullYear() <= 1) return "-";
  return date.toLocaleString();
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
    toast("已复制");
  } catch {
    toast("复制失败", true);
  }
}

async function openRdpTarget(target) {
  $("rdpTarget").value = target;
  await openRdp($("openRdp"));
}

async function checkRdpTarget(target) {
  const device = typeof target === "object" ? target : { virtual_ip: target };
  if (device.node_id || device.virtual_ip) selectDevice(device);
  $("rdpTarget").value = device.virtual_ip || "";
  await checkRdp($("checkRdp"));
}

async function renameSelectedDevice(button) {
  const device = selectedDevice();
  if (!canAdminDevice(device)) return toast("请选择远端设备", true);
  const displayName = $("deviceDisplayName").value.trim();
  if (!displayName) return toast("请输入设备显示名称", true);
  setBusy(button, true);
  try {
    await api("/api/onboarding/device/rename", {
      method: "POST",
      body: JSON.stringify({ node_id: device.node_id, display_name: displayName }),
    });
    toast("设备已重命名");
    await refreshDevices();
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function disableSelectedDevice(button) {
  const device = selectedDevice();
  if (!canAdminDevice(device)) return toast("请选择远端设备", true);
  if (!window.confirm(`禁用设备 ${device.display_name || device.node_id}？该设备将不能重新连接。`)) return;
  setBusy(button, true);
  try {
    await api("/api/onboarding/device/disable", {
      method: "POST",
      body: JSON.stringify({ node_id: device.node_id }),
    });
    toast("设备已禁用");
    await refreshDevices();
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function removeSelectedDevice(button) {
  const device = selectedDevice();
  if (!canAdminDevice(device)) return toast("请选择远端设备", true);
  if (!window.confirm(`移除设备 ${device.display_name || device.node_id}？它将不再作为正常设备显示。`)) return;
  setBusy(button, true);
  try {
    await api("/api/onboarding/device/remove", {
      method: "POST",
      body: JSON.stringify({ node_id: device.node_id }),
    });
    toast("设备已移除");
    state.selectedDeviceKey = "";
    state.selectedDevice = null;
    await refreshDevices();
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function initCA(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/certs/init-ca", {
      method: "POST",
      body: JSON.stringify({
        out_dir: $("certOut").value,
        name: $("caName").value,
        days: 3650,
      }),
    });
    toast(`已创建：${data.result.files.join(", ")}`);
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function issueCert(button) {
  setBusy(button, true);
  try {
    const out = $("certOut").value;
    const data = await api("/api/certs/issue", {
      method: "POST",
      body: JSON.stringify({
        out_dir: out,
        name: $("nodeName").value,
        ca_path: `${out}\\ca.pem`,
        ca_key_path: `${out}\\ca-key.pem`,
        dns: $("dnsSans").value,
        ips: $("ipSans").value,
        days: Number($("certDays").value || 825),
      }),
    });
    toast(`已签发：${data.result.files.join(", ")}`);
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function loadConfig(button) {
  setBusy(button, true);
  try {
    const path = encodeURIComponent($("configPath").value);
    const data = await api(`/api/config?path=${path}`);
    $("configEditor").value = data.content;
    toast("配置已读取");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function saveConfig(button) {
  setBusy(button, true);
  try {
    const data = await api("/api/config", {
      method: "POST",
      body: JSON.stringify({
        path: $("configPath").value,
        content: $("configEditor").value,
      }),
    });
    $("configEditor").value = data.content;
    toast("配置已保存");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function loadLogs(button) {
  setBusy(button, true);
  try {
    const params = new URLSearchParams({
      config_path: $("configPath").value,
      service_name: $("serviceName").value,
    });
    const data = await api(`/api/logs?${params}`);
    $("logs").textContent = data.content || `没有日志内容：${data.path}`;
  } catch (err) {
    $("logs").textContent = err.message;
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function openRdp(button) {
  setBusy(button, true);
  try {
    await api("/api/rdp/open", {
      method: "POST",
      body: JSON.stringify({
        target: $("rdpTarget").value,
      }),
    });
    toast("远程桌面已打开");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function checkRdp(button) {
  setBusy(button, true);
  try {
    const selected = selectedDevice();
    const params = new URLSearchParams({
      target: $("rdpTarget").value,
      target_device: selected?.node_id || "",
      network_state: state.networkState,
      target_status: selected?.status || "",
    });
    const data = await api(`/api/diagnostics/rdp?${params}`);
    const diagnostics = $("diagnostics");
    const rdpDiagnostics = $("rdpDiagnostics");
    if (diagnostics) renderChecks([data.check], diagnostics);
    if (rdpDiagnostics) renderChecks([data.check], rdpDiagnostics);
    toast(data.check.status === "ok" ? "RDP 可达" : "RDP 不可达", data.check.status !== "ok");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

async function runOneClickDiagnostics(button) {
  setBusy(button, true);
  try {
    const params = oneClickDiagnosticParams();
    const data = await api(`/api/diagnostics/report?${params}`);
    renderDiagnosticReport(data.report, $("diagnosticReport"));
    toast(data.report?.status === "ok" ? "诊断完成，未发现问题" : "诊断完成，请查看报告", data.report?.status === "fail");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function oneClickDiagnosticParams() {
  const params = new URLSearchParams({
    config_path: $("configPath").value,
    service_name: $("serviceName").value,
    network_state: state.networkState,
  });
  const selected = selectedDevice();
  if (!selected) return params;
  const target = selected.virtual_ip || "";
  if (target) {
    params.set("target", target);
    params.set("include_rdp", "1");
  }
  params.set("target_device", selected.display_name || selected.node_id || "");
  params.set("target_status", selected.status || "");
  params.set("path_type", selected.path_type || "");
  params.set("path_state", selected.path_state || "");
  params.set("quality_score", String(selected.quality_score || 0));
  params.set("latency_ms", String(selected.latency_ms || 0));
  params.set("switch_count", String(selected.switch_count || 0));
  params.set("last_error", selected.last_error || "");
  return params;
}

async function runDiagnostics(button) {
  setBusy(button, true);
  try {
    const params = new URLSearchParams({
      config_path: $("configPath").value,
      service_name: $("serviceName").value,
    });
    const data = await api(`/api/diagnostics?${params}`);
    renderChecks(data.report.checks || [], $("diagnostics"));
    toast("诊断完成");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function renderDiagnosticReport(report, container) {
  if (!container) return;
  const findings = report?.findings || [];
  const headline = document.createElement("div");
  headline.className = "diagnostic-head";
  const badge = document.createElement("span");
  badge.className = `badge ${report?.status || "ok"}`;
  badge.textContent = statusText(report?.status || "ok");
  const title = document.createElement("strong");
  title.textContent = report?.summary?.headline || "诊断完成";
  headline.append(badge, title);

  if (!findings.length) {
    const empty = emptyState(report?.summary?.headline || "未发现需要处理的问题。");
    container.replaceChildren(headline, empty);
    return;
  }

  container.replaceChildren(
    headline,
    ...findings.map((finding) => {
      const card = document.createElement("article");
      card.className = "finding";

      const findingHead = document.createElement("div");
      findingHead.className = "finding-head";
      const severity = document.createElement("span");
      severity.className = `badge ${finding.severity || "warn"}`;
      severity.textContent = statusText(finding.severity || "warn");
      const name = document.createElement("strong");
      name.textContent = finding.title || "诊断发现";
      findingHead.append(severity, name);

      card.append(
        findingHead,
        findingLine("发现的问题", finding.problem),
        findingLine("影响原因", finding.impact),
        findingLine("建议处理", finding.recommendation),
        findingLine("下一步动作", finding.action),
      );
      return card;
    }),
  );
}

function findingLine(label, value) {
  const row = document.createElement("p");
  const name = document.createElement("strong");
  name.textContent = `${label}：`;
  row.append(name, document.createTextNode(value || "-"));
  return row;
}

function renderChecks(checks, container) {
  if (!checks.length) {
    container.textContent = "暂无诊断结果";
    return;
  }
  container.replaceChildren(
    ...checks.map((check) => {
      const row = document.createElement("div");
      row.className = "check";

      const badge = document.createElement("span");
      badge.className = `badge ${check.status}`;
      badge.textContent = statusText(check.status);

      const body = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = check.name;
      const detail = document.createElement("small");
      detail.textContent = check.detail || "";
      body.append(title, detail);

      row.append(badge, body);
      return row;
    }),
  );
}

function showView(id) {
  document.querySelectorAll(".view").forEach((view) => view.classList.toggle("active", view.id === id));
  document.querySelectorAll(".entry-card").forEach((button) => button.classList.toggle("active", button.dataset.view === id));
}

function togglePlanComparison() {
  const section = $("planComparisonSection");
  if (!section) return;
  section.hidden = !section.hidden;
}

function selectedDevice() {
  return state.selectedDevice;
}

function bindClick(id, handler) {
  const el = $(id);
  if (el) el.addEventListener("click", handler);
}

document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof HTMLButtonElement)) return;
  const action = target.dataset.action;
  if (action) {
    serviceAction(action, target).catch(() => {});
  }
});

document.querySelectorAll(".entry-card").forEach((button) => {
  button.addEventListener("click", () => showView(button.dataset.view));
});

bindClick("refreshStatus", () => refreshStatus().catch((err) => toast(err.message, true)));
bindClick("startServer", (event) => startServer(event.currentTarget));
bindClick("stopServer", (event) => stopServer(event.currentTarget));
bindClick("regenerateInvite", (event) => regenerateInvite(event.currentTarget));
bindClick("officialCreateAccount", (event) => officialCreateAccount(event.currentTarget));
bindClick("officialCreateNetwork", (event) => officialCreateNetwork(event.currentTarget));
bindClick("officialCreateInvite", (event) => officialCreateInvite(event.currentTarget));
bindClick("officialJoinDevice", (event) => officialJoinDevice(event.currentTarget));
bindClick("officialHeartbeat", (event) => officialHeartbeat(event.currentTarget));
bindClick("officialRefreshDevices", (event) => officialRefreshDevices(event.currentTarget));
bindClick("officialCreateOrganization", (event) => officialCreateOrganization(event.currentTarget));
bindClick("officialRefreshTeam", (event) => officialRefreshTeam(event.currentTarget));
bindClick("officialChangeMemberRole", (event) => officialChangeMemberRole(event.currentTarget));
bindClick("officialCreateGroup", (event) => officialCreateGroup(event.currentTarget));
bindClick("officialRenameGroup", (event) => officialRenameGroup(event.currentTarget));
bindClick("officialDeleteGroup", (event) => officialDeleteGroup(event.currentTarget));
bindClick("officialEnrollTeamDevice", (event) => officialEnrollTeamDevice(event.currentTarget));
bindClick("officialAddTeamDeviceGroup", (event) => officialChangeTeamDeviceGroup(event.currentTarget, "add"));
bindClick("officialRemoveTeamDeviceGroup", (event) => officialChangeTeamDeviceGroup(event.currentTarget, "remove"));
bindClick("officialGrantConnection", (event) => officialGrantConnection(event.currentTarget));
bindClick("officialAuditQuery", (event) => officialQueryAudit(event.currentTarget, "reset"));
bindClick("officialAuditPrevious", (event) => officialQueryAudit(event.currentTarget, "previous"));
bindClick("officialAuditNext", (event) => officialQueryAudit(event.currentTarget, "next"));
bindClick("officialAuditCleanup", (event) => officialCleanupAudit(event.currentTarget));
bindClick("officialCreateDeploymentBundle", (event) => officialCreateDeploymentBundle(event.currentTarget));
bindClick("officialRevokeDeploymentCredential", (event) => officialRevokeDeploymentCredential(event.currentTarget));
bindClick("officialRefreshDeployments", (event) => officialRefreshDeployments(event.currentTarget));
bindClick("officialCreateRollout", (event) => officialCreateRollout(event.currentTarget));
bindClick("officialViewRollout", (event) => officialViewRollout(event.currentTarget));
bindClick("officialCancelRollout", (event) => officialCancelRollout(event.currentTarget));
bindClick("officialRefreshPrivateLicense", (event) => officialRefreshPrivateLicense(event.currentTarget));
bindClick("officialImportPrivateLicense", (event) => officialImportPrivateLicense(event.currentTarget));
bindClick("viewPlans", () => togglePlanComparison());
bindClick("joinNetwork", (event) => joinNetwork(event.currentTarget));
bindClick("disconnectNetwork", (event) => disconnectNetwork(event.currentTarget));
bindClick("exitNetwork", (event) => exitNetwork(event.currentTarget));
bindClick("refreshDevices", () => refreshDevices().catch((err) => toast(err.message, true)));
bindClick("renameDevice", (event) => renameSelectedDevice(event.currentTarget));
bindClick("disableDevice", (event) => disableSelectedDevice(event.currentTarget));
bindClick("removeDevice", (event) => removeSelectedDevice(event.currentTarget));
bindClick("copyInviteLink", () => copyText($("accessLink").value));
bindClick("copyInviteCode", () => copyText($("accessCode").textContent));
bindClick("initCA", (event) => initCA(event.currentTarget));
bindClick("issueCert", (event) => issueCert(event.currentTarget));
bindClick("loadConfig", (event) => loadConfig(event.currentTarget));
bindClick("saveConfig", (event) => saveConfig(event.currentTarget));
bindClick("loadLogs", (event) => loadLogs(event.currentTarget));
bindClick("openRdp", (event) => openRdp(event.currentTarget));
bindClick("checkRdp", (event) => checkRdp(event.currentTarget));
bindClick("runOneClickDiagnostics", (event) => runOneClickDiagnostics(event.currentTarget));
bindClick("runDiagnostics", (event) => runDiagnostics(event.currentTarget));

loadInfo()
  .then(async () => {
    const initialLoads = [refreshStatus(), refreshDevices()];
    if (state.officialHubEnabled) {
      initialLoads.push(loadOfficialHubState(), loadSubscriptionExperience());
    }
    await Promise.allSettled(initialLoads);
    startStatusPolling();
  })
  .catch((err) => toast(err.message, true));
