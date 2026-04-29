const state = {
  disks: [],
  selected: null,
  scan: null,
  events: null,
};

const els = {
  status: document.querySelector("#serviceStatus"),
  diskList: document.querySelector("#diskList"),
  selectedName: document.querySelector("#selectedName"),
  refreshBtn: document.querySelector("#refreshBtn"),
  startBtn: document.querySelector("#startBtn"),
  stopBtn: document.querySelector("#stopBtn"),
  blockSize: document.querySelector("#blockSize"),
  progressText: document.querySelector("#progressText"),
  progressBar: document.querySelector("#progressBar"),
  badBlocks: document.querySelector("#badBlocks"),
  elapsedText: document.querySelector("#elapsedText"),
  errRead: document.querySelector("#errRead"),
  errWrite: document.querySelector("#errWrite"),
  errCompare: document.querySelector("#errCompare"),
  scanStatus: document.querySelector("#scanStatus"),
  surface: document.querySelector("#surface"),
  log: document.querySelector("#log"),
};

function setStats(scan) {
  els.elapsedText.textContent = scan.elapsed || "0:00";
  els.errRead.textContent = scan.errRead || 0;
  els.errWrite.textContent = scan.errWrite || 0;
  els.errCompare.textContent = scan.errCompare || 0;
}

function createSurface() {
  els.surface.innerHTML = "";
  for (let i = 0; i < 500; i += 1) {
    const cell = document.createElement("div");
    cell.className = "cell";
    els.surface.appendChild(cell);
  }
}

function setProgress(value, badBlocks = 0) {
  const progress = Math.max(0, Math.min(100, Number(value) || 0));
  els.progressText.textContent = `${progress.toFixed(progress >= 10 ? 1 : 2)}%`;
  els.progressBar.style.width = `${progress}%`;
  els.badBlocks.textContent = String(badBlocks || 0);
  const cells = [...els.surface.children];
  const done = Math.floor((progress / 100) * cells.length);
  cells.forEach((cell, index) => {
    cell.className = "cell";
    if (index < done) cell.classList.add("done");
    if (index === done && progress > 0 && progress < 100) cell.classList.add("active");
  });
  for (let i = 0; i < Math.min(badBlocks, cells.length); i += 1) {
    cells[(i * 37) % cells.length].className = "cell bad";
  }
}

async function fetchJSON(url, options) {
  const res = await fetch(url, options);
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || `HTTP ${res.status}`);
  return data;
}

async function loadDisks() {
  els.status.textContent = "读取中";
  els.diskList.innerHTML = "<div class=\"disk\"><span>正在读取系统硬盘...</span></div>";
  try {
    const data = await fetchJSON("/api/disks");
    state.disks = data.disks || [];
    els.status.textContent = data.badblocksAvailable ? "badblocks 可用" : "未找到 badblocks";
    renderDisks();
  } catch (err) {
    els.status.textContent = "服务异常";
    els.diskList.innerHTML = `<div class="disk"><span>${escapeHTML(err.message)}</span></div>`;
  }
}

function renderDisks() {
  if (!state.disks.length) {
    els.diskList.innerHTML = "<div class=\"disk\"><span>没有检测到硬盘</span></div>";
    return;
  }
  els.diskList.innerHTML = "";
  state.disks.forEach((disk) => {
    const btn = document.createElement("button");
    btn.className = `disk ${state.selected?.path === disk.path ? "active" : ""}`;
    btn.innerHTML = `
      <strong>${escapeHTML(disk.path)} · ${escapeHTML(disk.sizeHuman)}</strong>
      <span>${escapeHTML(disk.model || "Unknown")} ${escapeHTML(disk.serial || "")}</span>
      <span>${disk.rotational ? "HDD" : "SSD/NVMe"} · ${escapeHTML(disk.transport || "-")} · ${escapeHTML(disk.mountpoint || "未挂载")}</span>
    `;
    btn.addEventListener("click", () => selectDisk(disk));
    els.diskList.appendChild(btn);
  });
}

function selectDisk(disk) {
  state.selected = disk;
  els.selectedName.textContent = `${disk.path} · ${disk.model || "Unknown"} · ${disk.sizeHuman}`;
  els.startBtn.disabled = false;
  renderDisks();
}

async function startScan() {
  if (!state.selected) return;
  if (state.events) state.events.close();
  els.log.textContent = "";
  setProgress(0, 0);
  els.scanStatus.textContent = "启动中";
  els.startBtn.disabled = true;
  els.stopBtn.disabled = false;
  try {
    const scan = await fetchJSON("/api/scan", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        path: state.selected.path,
        blockSize: Number(els.blockSize.value),
      }),
    });
    state.scan = scan;
    connectEvents(scan.id);
  } catch (err) {
    els.scanStatus.textContent = "启动失败";
    appendLog(err.message);
    els.startBtn.disabled = false;
    els.stopBtn.disabled = true;
  }
}

function connectEvents(id) {
  const source = new EventSource(`/api/scans/${id}/events`);
  state.events = source;
  source.addEventListener("line", (event) => appendLog(event.data));
  source.addEventListener("state", (event) => {
    const scan = JSON.parse(event.data);
    state.scan = scan;
    els.scanStatus.textContent = statusLabel(scan.status);
    setProgress(scan.progress, (scan.badBlocks || 0) + (scan.errRead || 0) + (scan.errWrite || 0) + (scan.errCompare || 0));
    setStats(scan);
    if (scan.status !== "running") {
      els.startBtn.disabled = !state.selected;
      els.stopBtn.disabled = true;
      source.close();
    }
  });
  source.onerror = () => {
    appendLog("实时连接中断，正在等待服务端状态。");
  };
}

async function stopScan() {
  if (!state.scan) return;
  els.stopBtn.disabled = true;
  await fetchJSON(`/api/scans/${state.scan.id}/stop`, { method: "POST" }).catch((err) => appendLog(err.message));
}

function statusLabel(status) {
  return {
    running: "扫描中",
    finished: "完成",
    failed: "失败",
    stopped: "已停止",
  }[status] || "待机";
}

function appendLog(line) {
  els.log.textContent += `${line}\n`;
  els.log.scrollTop = els.log.scrollHeight;
}

function escapeHTML(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#039;",
  }[ch]));
}

els.refreshBtn.addEventListener("click", loadDisks);
els.startBtn.addEventListener("click", startScan);
els.stopBtn.addEventListener("click", stopScan);

createSurface();
loadDisks();
