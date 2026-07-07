const $ = (id) => document.getElementById(id);

const state = {
  cwd: "",
  invite: null,
  inviteTimer: null,
  selectedDeviceKey: "",
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
    throw new Error(data.error || `Request failed: ${res.status}`);
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
  }[status] || status || "未知";
}

async function loadInfo() {
  const [info, defaults] = await Promise.all([api("/api/info"), api("/api/onboarding/defaults")]);
  state.cwd = info.cwd;
  if ($("configPath")) $("configPath").value = defaults.config_path;
  $("listenPort").value = String(defaults.listen_port || 8443);
  const certOut = $("certOut");
  if (certOut) certOut.value = `${info.cwd}\\certs`;
}

async function refreshStatus() {
  const name = encodeURIComponent($("serviceName").value);
  const data = await api(`/api/service/status?service_name=${name}`);
  const status = data.status;
  const label = $("serviceState");
  label.textContent = serviceStateText(status);
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
  await refreshStatus();
}

async function startServer(button) {
  setBusy(button, true);
  $("serverStatus").textContent = "正在准备";
  try {
    const data = await api("/api/onboarding/start-server", {
      method: "POST",
      body: JSON.stringify({
        server_address: $("serverAddress").value.trim(),
        listen_port: Number($("listenPort").value || 8443),
        long_lived: $("longLivedCode").checked,
      }),
    });
    const result = data.result;
    $("configPath").value = result.config_path;
    $("serverStatus").textContent = "配置已生成";
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
        long_lived: $("longLivedCode").checked,
        replace_existing: true,
      }),
    });
    renderInvite(data.invite);
    if ($("configPath").value) {
      await installAndStartAgent($("configPath").value);
    }
    toast("接入码已重新生成，服务已重启");
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
    label.textContent = "默认 10 分钟有效，使用一次失效";
    return;
  }
  if (invite.long_lived) {
    label.textContent = "长期有效";
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

async function joinNetwork(button) {
  setBusy(button, true);
  $("clientStatus").textContent = "正在加入";
  try {
    const data = await api("/api/onboarding/join", {
      method: "POST",
      body: JSON.stringify({
        invite_link: inviteLinkForJoin(),
        code: $("inviteCode").value,
      }),
    });
    const result = data.result;
    $("configPath").value = result.config_path;
    $("clientStatus").textContent = "配置已生成";
    try {
      await installAndStartAgent(result.config_path);
      $("clientStatus").textContent = "已加入并运行";
      toast("已加入网络");
    } catch (err) {
      $("clientStatus").textContent = "服务启动失败";
      toast(`已加入网络，服务启动失败：${err.message}`, true);
    }
    await refreshDevices();
  } catch (err) {
    $("clientStatus").textContent = "加入失败";
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
}

function inviteLinkForJoin() {
  const raw = $("inviteLink").value.trim();
  const manualServer = $("manualServerAddress").value.trim();
  if (!manualServer) return raw;
  const link = new URL(raw);
  link.searchParams.set("server", manualServer);
  return link.toString();
}

async function exitNetwork(button) {
  try {
    await serviceAction("stop", button);
    $("clientStatus").textContent = "已退出";
    await refreshDevices();
  } catch {
    $("clientStatus").textContent = "退出失败";
  }
}

async function refreshDevices() {
  const params = new URLSearchParams({ service_name: $("serviceName").value });
  const data = await api(`/api/onboarding/devices?${params}`);
  renderDevices(data.devices.nodes || []);
}

function renderDevices(devices) {
  const list = $("deviceList");
  if (!devices.length) {
    list.replaceChildren(emptyState("暂无节点"));
    $("deviceDetail").textContent = "服务器或客户端启动后，节点会显示在这里。";
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

  const dot = document.createElement("span");
  dot.className = `dot ${device.status === "online" ? "online" : "offline"}`;

  const main = document.createElement("div");
  const title = document.createElement("strong");
  title.textContent = `${device.node_id || "未命名节点"}${device.virtual_ip ? `  ${device.virtual_ip}` : ""}`;
  const detail = document.createElement("small");
  const parts = [deviceStatusText(device.status, device.kind)];
  if (device.remote_addr) parts.push(`来源 ${sourceIP(device.remote_addr)}`);
  if (device.fingerprint) parts.push(device.fingerprint);
  detail.textContent = parts.join(" · ");
  main.append(title, detail);

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
  rdp.disabled = !device.virtual_ip || device.kind === "self";
  rdp.addEventListener("click", (event) => {
    event.stopPropagation();
    openRdpTarget(device.virtual_ip);
  });

  actions.append(rdp, copy);
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
  document.querySelectorAll(".device-row").forEach((row) => {
    row.classList.toggle("selected", row.dataset.key === state.selectedDeviceKey);
  });
  if (device.virtual_ip) $("rdpTarget").value = device.virtual_ip;
  const detail = $("deviceDetail");
  const lines = [
    `节点名称：${device.node_id || "未命名节点"}`,
    `虚拟 IP：${device.virtual_ip || "-"}`,
    `在线状态：${deviceStatusText(device.status, device.kind)}`,
    `来源 IP：${device.remote_addr ? sourceIP(device.remote_addr) : "-"}`,
    `最近在线：${formatTime(device.last_seen)}`,
    `证书指纹：${device.fingerprint || "-"}`,
  ];
  detail.textContent = lines.join("\n");
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
  $("rdpTarget").value = target;
  await checkRdp($("checkRdp"));
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
    const params = new URLSearchParams({ target: $("rdpTarget").value });
    const data = await api(`/api/diagnostics/rdp?${params}`);
    const diagnostics = $("diagnostics");
    if (diagnostics) renderChecks([data.check], diagnostics);
    toast(data.check.status === "ok" ? "RDP 可达" : "RDP 不可达", data.check.status !== "ok");
  } catch (err) {
    toast(err.message, true);
  } finally {
    setBusy(button, false);
  }
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
  document.querySelectorAll(".mode").forEach((button) => button.classList.toggle("active", button.dataset.view === id));
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

document.querySelectorAll(".mode").forEach((button) => {
  button.addEventListener("click", () => showView(button.dataset.view));
});

bindClick("refreshStatus", () => refreshStatus().catch((err) => toast(err.message, true)));
bindClick("startServer", (event) => startServer(event.currentTarget));
bindClick("stopServer", (event) => stopServer(event.currentTarget));
bindClick("regenerateInvite", (event) => regenerateInvite(event.currentTarget));
bindClick("joinNetwork", (event) => joinNetwork(event.currentTarget));
bindClick("exitNetwork", (event) => exitNetwork(event.currentTarget));
bindClick("refreshDevices", () => refreshDevices().catch((err) => toast(err.message, true)));
bindClick("copyInviteLink", () => copyText($("accessLink").value));
bindClick("copyInviteCode", () => copyText($("accessCode").textContent));
bindClick("initCA", (event) => initCA(event.currentTarget));
bindClick("issueCert", (event) => issueCert(event.currentTarget));
bindClick("loadConfig", (event) => loadConfig(event.currentTarget));
bindClick("saveConfig", (event) => saveConfig(event.currentTarget));
bindClick("loadLogs", (event) => loadLogs(event.currentTarget));
bindClick("openRdp", (event) => openRdp(event.currentTarget));
bindClick("checkRdp", (event) => checkRdp(event.currentTarget));
bindClick("runDiagnostics", (event) => runDiagnostics(event.currentTarget));

loadInfo()
  .then(() => Promise.allSettled([refreshStatus(), refreshDevices()]))
  .catch((err) => toast(err.message, true));
