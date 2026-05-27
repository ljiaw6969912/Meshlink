const $ = (id) => document.getElementById(id);

const state = {
  cwd: "",
};

function setBusy(button, busy) {
  button.disabled = busy;
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

async function loadInfo() {
  const data = await api("/api/info");
  state.cwd = data.cwd;
  $("cwd").textContent = data.cwd;
  if (!$("configPath").value) {
    $("configPath").value = `${data.cwd}\\configs\\spoke.example.json`;
  }
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
    const params = new URLSearchParams({ target: $("rdpTarget").value });
    const data = await api(`/api/diagnostics/rdp?${params}`);
    renderChecks([data.check], $("diagnostics"));
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

document.addEventListener("click", (event) => {
  const target = event.target;
  if (!(target instanceof HTMLButtonElement)) return;
  const action = target.dataset.action;
  if (action) {
    serviceAction(action, target);
  }
});

$("refreshStatus").addEventListener("click", () => refreshStatus().catch((err) => toast(err.message, true)));
$("initCA").addEventListener("click", (event) => initCA(event.currentTarget));
$("issueCert").addEventListener("click", (event) => issueCert(event.currentTarget));
$("loadConfig").addEventListener("click", (event) => loadConfig(event.currentTarget));
$("saveConfig").addEventListener("click", (event) => saveConfig(event.currentTarget));
$("loadLogs").addEventListener("click", (event) => loadLogs(event.currentTarget));
$("openRdp").addEventListener("click", (event) => openRdp(event.currentTarget));
$("checkRdp").addEventListener("click", (event) => checkRdp(event.currentTarget));
$("runDiagnostics").addEventListener("click", (event) => runDiagnostics(event.currentTarget));

loadInfo()
  .then(() => Promise.allSettled([refreshStatus(), loadConfig($("loadConfig"))]))
  .catch((err) => toast(err.message, true));
