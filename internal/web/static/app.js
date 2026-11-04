"use strict";
// FlexWSPR GUI: talks to the same JSON API in the browser and in the desktop app.

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = (s) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
const mhz = (hz) => (hz / 1e6).toFixed(6);
const hhmm = (t) => new Date(t).toISOString().slice(11, 16);
const clone = (o) => JSON.parse(JSON.stringify(o));

let cfg = null;        // last saved configuration
let local = false;     // desktop app: program paths are editable
let bands = [];        // [{name, dial_hz}]
let sched = null;      // schedule being edited {mode, rotation, slots}
let defBand = "20m";
let schedDirty = false;
let lastSpotKey = "";
let status = null;

async function api(path, body) {
  const opt = body === undefined ? {} : { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) };
  const r = await fetch(path, opt);
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || `${r.status} ${r.statusText}`);
  return j;
}

// ---------- tabs ----------
$$(".tabs button").forEach((b) => b.addEventListener("click", () => showTab(b.dataset.tab)));
function showTab(name) {
  $$(".tabs button").forEach((b) => b.classList.toggle("on", b.dataset.tab === name));
  $$(".tab").forEach((t) => t.classList.toggle("on", t.id === "tab-" + name));
  try { localStorage.setItem("tab", name); } catch {}
  if (name === "log") pollLog();
  if (name === "openings") pollOpenings(true);
}

// ---------- clock ----------
function tick() {
  const now = new Date();
  $("#utc").textContent = now.toISOString().slice(11, 19);
  const ms = now.getTime();
  const next = Math.ceil((ms + 1) / 120000) * 120000;
  const left = Math.round((next - ms) / 1000);
  $("#countdown").textContent = `${Math.floor(left / 60)}:${String(left % 60).padStart(2, "0")}`;
}
setInterval(tick, 500);
tick();

// ---------- status ----------
const stateWords = {
  setup: "Needs setup", searching: "Searching", connecting: "Connecting",
  running: "Running", error: "Retrying", stopped: "Stopped",
};

async function pollStatus() {
  try {
    status = await api("/api/status");
    renderStatus(status);
  } catch (e) {
    $("#state").textContent = "No connection";
    $("#state").className = "pill error";
  }
}

function renderStatus(s) {
  $("#state").textContent = stateWords[s.state] || s.state;
  $("#state").className = "pill " + s.state;
  $("#band").textContent = s.band || "–";
  $("#helpVersion").textContent = "version " + s.version;
  $("#dial").textContent = (s.dial_hz ? mhz(s.dial_hz) + " MHz" : "") + (s.rxant ? " · " + antLabel(s.rxant) : "");
  $("#radio").textContent = s.radio || "–";
  $("#stateText").textContent = s.error || (s.since ? `${stateWords[s.state] || s.state} since ${hhmm(s.since)} UTC` : "");
  $("#station").textContent = cfg && cfg.callsign ? `${cfg.callsign} · ${cfg.grid}` : "";

  const banner = $("#banner");
  if (s.state === "setup") {
    banner.innerHTML = "Welcome. Enter your callsign and grid in <b>Settings</b> and press <b>Save and restart</b> to start receiving.";
    banner.hidden = false;
  } else if (!s.config_saved) {
    banner.textContent = "Running on default settings. Save them in Settings to keep them.";
    banner.hidden = false;
  } else banner.hidden = true;

  // Coming up
  const next = s.next || [];
  let prev = s.band;
  $("#next").innerHTML = next.slice(0, 6).map((p) => {
    const ch = p.band !== prev; prev = p.band;
    return `<span class="${ch ? "change" : ""}" title="${hhmm(p.start)} UTC">${esc(p.band)}</span>`;
  }).join("") || "–";
  $("#schedState").textContent = cfg ? ({ rotation: "Band rotation", slots: "Time slots" }[cfg.schedule.mode] || "One band") : "";

  // Totals
  const bs = s.band_spots || {};
  const total = Object.values(bs).reduce((a, b) => a + b, 0);
  $("#spotTotal").textContent = total;
  $("#bandSpots").textContent = Object.entries(bs).sort((a, b) => b[1] - a[1]).map(([b, n]) => `${b} ${n}`).join(" · ");
  $("#upload").textContent = s.upload ? `${s.uploaded} sent` : "off";
  $("#uploadQ").textContent = s.upload ? (s.upload_queue ? `${s.upload_queue} waiting to send` : "queue empty") : "turn on in Settings";

  // Best signal: highest score since start (see Score in status.go)
  const watts = (dbm) => { const w = Math.pow(10, (dbm - 30) / 10); return w >= 1 ? w.toFixed(w < 10 ? 1 : 0) + " W" : Math.round(w * 1000) + " mW"; };
  if (s.best) {
    const b = s.best;
    $("#bestText").innerHTML = `<b>${esc(b.call)}</b> ${esc(b.grid)} · ${esc(b.band)} · ${b.km.toLocaleString()} km · ` +
      `${b.snr} dB from ${watts(b.dbm)} · score <span class="score">${fmtScore(b.score)} dB</span> · ${hhmm(b.time)} UTC`;
    $("#bestText").classList.remove("muted");
  }
  const bb = s.best_by_band || {};
  const order = bands.map((x) => x.name).filter((n) => bb[n]);
  $("#bestBands").textContent = order.length > 1 ? "Per band: " + order.map((n) => `${n} ${bb[n].call} ${fmtScore(bb[n].score)}`).join(" · ") : "";

  // Cycles
  $("#cycles tbody").innerHTML = (s.cycles || []).map((c) =>
    `<tr title="${c.error ? esc(c.error) : c.gaps ? c.gaps + " samples filled for lost packets" : ""}">
      <td class="mono">${hhmm(c.start)}</td><td>${esc(c.band)}</td>
      <td class="r ${c.error ? "err" : ""}">${c.error ? "error" : c.spots}</td></tr>`).join("");

  // Band filter options
  const sel = $("#spotBand");
  const have = new Set([...sel.options].map((o) => o.value));
  Object.keys(bs).forEach((b) => { if (!have.has(b)) sel.add(new Option(b, b)); });
  renderSpots();
  const key = (s.antennas || []).join(",");
  if (cfg && key && key !== antKey) { antKey = key; refreshAntennas(); }
}

// Rebuild the antenna menus from the radio's list, keeping unsaved choices.
let antKey = "";
function refreshAntennas() {
  const per = {};
  $$("#antgrid select").forEach((sel) => { if (sel.value) per[sel.dataset.band] = sel.value; });
  fillAntennas($("#settings").elements["rxant"].value, per);
}

function renderSpots() {
  if (!status) return;
  const f = $("#spotBand").value;
  const spots = (status.spots || []).filter((p) => !f || p.band === f);
  const key = spots.length ? spots[0].time + spots[0].call + spots.length : "";
  const newest = spots.length ? spots[0].time : null;
  $("#spots tbody").innerHTML = spots.map((p) =>
    `<tr class="${key !== lastSpotKey && p.time === newest ? "newest" : ""}">
      <td class="mono">${hhmm(p.time)}</td><td>${esc(p.band)}</td>
      <td class="call">${p.call ? esc(p.call) : '<span class="muted">&lt;…&gt;</span>'}</td>
      <td>${esc(p.grid)}</td><td class="r">${p.km || ""}</td>
      <td class="r">${p.snr}</td><td class="r">${p.dt.toFixed(1)}</td>
      <td class="r mono">${mhz(p.freq_hz)}</td><td class="r">${p.drift}</td><td class="r">${p.dbm}</td></tr>`).join("");
  lastSpotKey = key;
  $("#noSpots").hidden = spots.length > 0;
}
$("#spotBand").addEventListener("change", () => { lastSpotKey = ""; renderSpots(); });

// ---------- openings ----------
// What a band is doing. Heard here (one receiver): by the farthest spot.
// In the area: by how many paths are long, so one odd path does not count.
const level = (km, n) => (!n ? "quiet" : km >= 5000 ? "dx" : km >= 1000 ? "open" : "local");
// DX: 3+ paths over 5000 km making at least 5% of all; open: 3+ over
// 1000 km making at least 10%. Busy bands always have a few long paths.
const areaLevel = (a) => {
  const n = a ? a.p || a.paths || 0 : 0;
  return !n ? "quiet" : a.k5 >= 3 && a.k5 >= n * 0.05 ? "dx" : a.k1 >= 3 && a.k1 >= n * 0.1 ? "open" : "local";
};
const pct = (k, n) => Math.round((100 * k) / n) + "%";
const levelWord = { quiet: "Quiet", local: "Local", open: "Open", dx: "DX" };
const showBands = ["160m", "80m", "60m", "40m", "30m", "20m", "17m", "15m", "12m", "10m", "6m"];
const kmText = (n) => n.toLocaleString() + " km";
const ago = (t, now) => {
  const m = Math.round((new Date(now) - new Date(t)) / 60000);
  return m < 1 ? "just now" : m < 60 ? m + " min ago" : Math.floor(m / 60) + " h " + (m % 60) + " min ago";
};
const fmtScore = (v) => (v >= 0 ? "+" : "−") + Math.abs(v).toFixed(1);
let summary = null, area = null, areaErr = "";

async function pollOpenings(force) {
  if (force !== true && !$("#tab-openings").classList.contains("on")) return;
  try { summary = await api("/api/summary"); } catch {}
  try { area = await api("/api/area"); areaErr = area.error || ""; }
  catch (e) { area = null; areaErr = e.message; }
  renderOpenings();
}

function renderOpenings() {
  if (!summary) return;
  const now = summary.now;
  const here = Object.fromEntries((summary.bands || []).map((b) => [b.band, b]));
  const ab = Object.fromEntries(((area && area.bands) || []).map((b) => [b.band, b]));
  const pos = (b) => { const i = bands.findIndex((x) => x.name === b); return i < 0 ? 99 : i; };
  const list = [...new Set([...showBands, ...Object.keys(here).filter((b) => pos(b) < 99)])].sort((a, b) => pos(a) - pos(b));
  const cur = status && status.state === "running" ? status.band : "";

  // Tiles: the area's last 30 minutes (or this hour here), and what was heard here.
  $("#tiles").innerHTML = list.map((b) => {
    const a = ab[b], h = here[b];
    let lv, main, sub = "";
    if (area && area.bands) {
      lv = areaLevel(a);
      main = a ? `${a.paths.toLocaleString()} paths · ${a.tx} TX · ${a.rx} RX` : "no paths";
      if (a) sub = `${pct(a.k1, a.paths)} over 1000 km · ${pct(a.k5, a.paths)} over 5000`;
    } else {
      const hl = h && h.hours[23];
      lv = level(hl ? hl.km : 0, hl ? hl.s : 0);
      main = h && h.last_hour ? `${h.last_hour} spots in the last hour` : "nothing this hour";
    }
    const hereTxt = b === cur ? "● listening now"
      : !h ? "here: not listened to today"
      : h.last ? `here: last heard ${ago(h.last, now)}` : `here: ${h.cycles} cycles, nothing heard`;
    const tip = a && a.far ? `Longest path: ${a.far} (${kmText(a.max_km)})` : "";
    return `<div class="tile ${lv}${b === cur ? " cur" : ""}" title="${esc(tip)}">
      <div class="tb"><b>${esc(b)}</b><span>${levelWord[lv]}</span></div>
      <div class="tm">${esc(main)}</div><div class="ts">${esc(sub)}</div>
      <div class="th">${esc(hereTxt)}</div></div>`;
  }).join("");
  const off = cfg && cfg.area && !cfg.area.enabled;
  $("#areaNote").textContent = area && area.bands
    ? `Paths with either end within ${area.radius_km} km of ${area.grid}, last ${area.window_min} minutes, from wspr.live · updated ${hhmm(area.fetched)} UTC` + (areaErr ? " · " + areaErr : "")
    : off ? "Heard here only. Turn on area activity in Settings to see the bands you are not on."
    : "Heard here only, this hour" + (areaErr ? ` (wspr.live: ${areaErr})` : "");

  // Heat maps: one row per band, one column per UTC hour.
  const hours = summary.hours;
  const head = `<div class="hb"></div>` + hours.map((t, i) =>
    `<div class="hh">${i % 3 === 0 ? String(new Date(t).getUTCHours()).padStart(2, "0") : ""}</div>`).join("");
  const showArea = !!(area && area.day && area.hours);
  $("#areaPanel").hidden = !showArea;
  if (showArea) {
    const shift = Math.round((new Date(area.hours[0]) - new Date(hours[0])) / 3600000);
    $("#areaHeat").innerHTML = head + list.map((b) => `<div class="hb">${esc(b)}</div>` + hours.map((t, i) => {
      const cell = (area.day[b] || [])[i - shift];
      if (!cell || !cell.p) return `<div class="hc quiet" title="${b} ${hhmm(t)} UTC: no paths"></div>`;
      const n = cell.p >= 1000 ? Math.round(cell.p / 1000) + "k" : cell.p;
      return `<div class="hc ${areaLevel(cell)}" title="${b} ${hhmm(t)} UTC: ${cell.p} paths, ${cell.k1} over 1000 km, ${cell.k5} over 5000 km, farthest ${kmText(cell.km)}">${n}</div>`;
    }).join("")).join("");
    $("#areaHeatNote").textContent = `paths within ${area.radius_km} km each UTC hour`;
  }
  const hereBands = list.filter((b) => here[b]).concat(Object.keys(here).filter((b) => pos(b) === 99));
  $("#hereHeat").innerHTML = hereBands.length ? head + hereBands.map((b) => `<div class="hb">${esc(b)}</div>` + here[b].hours.map((cell, i) => {
    const t = hhmm(hours[i]);
    if (!cell.c && !cell.s) return `<div class="hc none" title="${b} ${t} UTC: not listening"></div>`;
    if (!cell.s) return `<div class="hc quiet" title="${b} ${t} UTC: ${cell.c} cycles, nothing heard">·</div>`;
    return `<div class="hc ${level(cell.km, cell.s)}" title="${b} ${t} UTC: ${cell.s} spots, ${cell.n} calls` +
      `${cell.km ? ", farthest " + kmText(cell.km) : ""}${cell.c ? " in " + cell.c + " cycles" : ""}">${cell.s}</div>`;
  }).join("")).join("") : `<p class="muted">Nothing decoded in the last 24 hours yet.</p>`;
  $("#hereNote").textContent = `${summary.cycles} cycles · ${summary.spots} spots · ${summary.calls} calls`;

  $("#bandSum tbody").innerHTML = (summary.bands || []).map((b) => `<tr>
    <td><b>${esc(b.band)}</b></td><td class="r">${b.cycles}</td><td class="r">${b.spots}</td><td class="r">${b.calls}</td><td class="r">${b.last_hour}</td>
    <td>${b.farthest ? `${esc(b.farthest.call)} ${kmText(b.farthest.km)}` : ""}</td>
    <td>${b.best ? `${esc(b.best.call)} ${fmtScore(b.best.score)}` : ""}</td>
    <td>${b.last ? hhmm(b.last) + " UTC" : ""}</td></tr>`).join("");
  const st = summary.stations || [];
  $("#stationNote").textContent = st.length ? `${st.length}, best score first` : "";
  $("#stations tbody").innerHTML = st.slice(0, 300).map((s) => `<tr>
    <td class="call">${esc(s.call)}</td><td>${esc(s.grid)}</td><td class="r">${s.km || ""}</td><td>${esc(s.bands.join(" "))}</td>
    <td class="r">${s.spots}</td><td class="r">${s.best_snr}</td><td class="r">${s.has_score ? fmtScore(s.score) : ""}</td>
    <td class="mono">${hhmm(s.last)}</td></tr>`).join("");
}

// ---------- log ----------
let logSeq = 0;
async function pollLog() {
  if (!$("#tab-log").classList.contains("on")) return;
  try {
    const j = await api("/api/log?since=" + logSeq);
    if (j.seq < logSeq) { logSeq = 0; $("#log").textContent = ""; }
    logSeq = j.seq;
    if (j.lines.length) {
      const pre = $("#log");
      const frag = document.createDocumentFragment();
      for (const l of j.lines) {
        const d = document.createElement("div");
        const m = l.match(/level=(\w+)/);
        if (m) d.className = m[1];
        d.textContent = l;
        frag.appendChild(d);
      }
      pre.appendChild(frag);
      while (pre.childNodes.length > 1000) pre.removeChild(pre.firstChild);
      if ($("#follow").checked) pre.scrollTop = pre.scrollHeight;
    }
  } catch {}
}

// ---------- settings ----------
const getPath = (o, p) => p.split(".").reduce((a, k) => (a == null ? a : a[k]), o);
function setPath(o, p, v) {
  const ks = p.split(".");
  let a = o;
  ks.slice(0, -1).forEach((k) => (a = a[k] ??= {}));
  a[ks.at(-1)] = v;
}

// Antennas: the radio's own list once connected, else the usual FLEX-6000 set.
const defaultAnts = ["ANT1", "ANT2", "RX_A", "RX_B", "XVTA", "XVTB"];
const antLabel = (a) => ({ RX_A: "RXa", RX_B: "RXb", XVTA: "XVTa", XVTB: "XVTb" }[a] || a);
const radioAnts = () => (status && status.antennas && status.antennas.length) ? status.antennas : null;

function antOptions(sel, value, blankText) {
  const list = [...(radioAnts() || defaultAnts)];
  if (value && !list.includes(value)) list.push(value);
  sel.innerHTML = `<option value="">${esc(blankText)}</option>` +
    list.map((a) => `<option value="${esc(a)}">${esc(antLabel(a))}</option>`).join("");
  sel.value = value || "";
}

function defaultText(v) { return "Default (" + (v ? antLabel(v) : "radio's choice") + ")"; }

function fillAntennas(rxant, per) {
  antOptions($("#settings").elements["rxant"], rxant, "Radio's choice");
  $("#antgrid").innerHTML = bands.map((b) => `<label><span>${b.name}</span><select data-band="${b.name}"></select></label>`).join("");
  $$("#antgrid select").forEach((sel) => antOptions(sel, per[sel.dataset.band], defaultText(rxant)));
  $("#antNote").textContent = radioAnts() ? "Antennas as reported by the radio." : "Connect to the radio to read its antenna list.";
}

// web.listen is ":8080" (all computers), "127.0.0.1:8080" (this one only)
// or "off". The form shows it as a port and a checkbox.
function fillWeb(listen) {
  const m = /^(.*):(\d+)$/.exec(listen || "");
  $("#webPort").value = m ? m[2] : "";
  $("#webLan").checked = m ? !/^(127\.0\.0\.1|localhost|\[::1\])$/.test(m[1]) : false;
  webNote();
}
function readWeb() {
  const p = parseInt($("#webPort").value, 10);
  if (!p) return "off";
  return ($("#webLan").checked ? "" : "127.0.0.1") + ":" + p;
}
function webNote() {
  const p = parseInt($("#webPort").value, 10);
  $("#webNote").textContent = !p ? "No web page."
    : $("#webLan").checked ? `Other computers: http://<this computer>:${p}/ (no password; keep it on your LAN).`
    : `This computer only: http://localhost:${p}/`;
  if (!local) $("#webNote").textContent += " Change it in the config file or the desktop app.";
}
$("#webPort").addEventListener("input", webNote);
$("#webLan").addEventListener("change", webNote);

function fillSettings() {
  const f = $("#settings");
  fillAntennas(cfg.rxant, cfg.antennas || {});
  for (const el of f.elements) {
    if (!el.name || el.name === "rxant") continue;
    const v = getPath(cfg, el.name);
    if (el.type === "checkbox") el.checked = !!v;
    else if (el.name === "wsprd.args") el.value = (v || []).join(" ");
    else el.value = v ?? "";
  }
  fillWeb(cfg.web.listen);
  $$(".sys", f).forEach((el) => (el.disabled = !local));
  $("#localNote").hidden = local;
  $("#cfgPath").textContent = status ? "Config file: " + status.config_path : "";
}

function readSettings() {
  const c = clone(cfg);
  for (const el of $("#settings").elements) {
    if (!el.name) continue;
    let v;
    if (el.type === "checkbox") v = el.checked;
    else if (el.name === "wsprd.args") v = el.value.trim() ? el.value.trim().split(/\s+/) : [];
    else if (el.name === "daxiq.channel" || el.name === "daxiq.rate" || el.name === "area.radius_km") v = parseInt(el.value, 10) || 0;
    else v = el.value.trim();
    setPath(c, el.name, v);
  }
  c.web.listen = readWeb();
  // The serial only picks a radio during discovery; with an IP set it would
  // just linger unseen, so drop it.
  if (c.radio.ip) c.radio.serial = "";
  c.antennas = {};
  $$("#antgrid select").forEach((sel) => { if (sel.value) c.antennas[sel.dataset.band] = sel.value; });
  return c;
}

async function save(c, errEl, btn) {
  errEl.textContent = "";
  btn.disabled = true;
  try {
    const j = await api("/api/config", c);
    cfg = j.config;
    local = j.local;
    fillSettings();
    loadSchedule();
    btn.textContent = "Saved ✓";
    setTimeout(() => (btn.textContent = btn.dataset.label), 1500);
    pollStatus();
    return true;
  } catch (e) {
    errEl.textContent = e.message;
    return false;
  } finally {
    btn.disabled = false;
  }
}

$("#settings").elements["rxant"].addEventListener("change", (e) => {
  $$("#antgrid select").forEach((sel) => (sel.options[0].text = defaultText(e.target.value)));
});

$("#saveSet").dataset.label = $("#saveSet").textContent;
$("#saveSet").addEventListener("click", () => {
  const f = $("#settings");
  if (!f.reportValidity()) return;
  save(readSettings(), $("#setErr"), $("#saveSet"));
});

$("#findRadios").addEventListener("click", async () => {
  const b = $("#findRadios");
  b.disabled = true;
  $("#findNote").textContent = "Listening for 3 s…";
  $("#radios").innerHTML = "";
  try {
    const list = await api("/api/radios");
    $("#findNote").textContent = list.length ? "Pick one:" : "No radios heard. Is it on this network?";
    $("#radios").innerHTML = list.map((r, i) =>
      `<button type="button" class="ghost" data-i="${i}"><b>${esc(r.Model)}</b> ${esc(r.Nickname)} · ${esc(r.IP)}
        <span class="muted small">serial ${esc(r.Serial)} · v${esc(r.Version)} · ${esc(r.Status)}</span></button>`).join("");
    $$("#radios button").forEach((btn) => btn.addEventListener("click", () => {
      const r = list[btn.dataset.i];
      $("#settings").elements["radio.ip"].value = r.IP;
    }));
  } catch (e) {
    $("#findNote").textContent = e.message;
  } finally {
    b.disabled = false;
  }
});

// ---------- schedule ----------
function loadSchedule() {
  sched = clone(cfg.schedule || {});
  sched.mode ||= "off";
  sched.slots ||= [];
  sched.rotation ||= {};
  sched.rotation.bands ||= [];
  sched.rotation.cycles ||= 3;
  defBand = cfg.band;
  schedDirty = false;
  renderSchedule();
}

function bandOptions(sel, value) {
  sel.innerHTML = bands.map((b) => `<option value="${b.name}">${b.name} · ${mhz(b.dial_hz)}</option>`).join("");
  if (!bands.some((b) => b.name === value)) sel.add(new Option(value + " MHz", value));
  sel.value = value;
}

const chipsHTML = (sel) => bands.map((b) => {
  const n = sel.indexOf(b.name);
  return `<button type="button" class="chip ${n >= 0 ? "on" : ""}" data-band="${b.name}">${n >= 0 ? `<span class="ord">${n + 1}</span>` : ""}${b.name}</button>`;
}).join("");

function toggleBand(list, b) {
  const n = list.indexOf(b);
  if (n >= 0) list.splice(n, 1); else list.push(b);
}

function renderRotation() {
  const r = sched.rotation;
  $("#rotBands").innerHTML = chipsHTML(r.bands);
  $("#rotCycles").value = r.cycles;
  const n = r.bands.length;
  if (!n) { $("#rotSummary").textContent = "Pick at least one band."; return; }
  const mins = n * r.cycles * 2;
  const h = mins >= 60 ? `${Math.floor(mins / 60)} h ${mins % 60 ? (mins % 60) + " min" : ""}`.trim() : `${mins} min`;
  $("#rotSummary").textContent = `${n} band${n > 1 ? "s" : ""} × ${r.cycles} cycle${r.cycles > 1 ? "s" : ""}: ` +
    `${r.cycles * 2} min on each band, back to each band every ${h}, ${Math.floor(1440 / mins * 10) / 10} visits per band per day.`;
}

function renderSchedule() {
  $$("input[name=mode]").forEach((r) => (r.checked = r.value === sched.mode));
  $("#rotPanel").hidden = sched.mode !== "rotation";
  $("#slotPanel").hidden = sched.mode !== "slots";
  bandOptions($("#defBand"), defBand);
  if (!$("#rotFrom").options.length) {
    const opts = bands.map((b) => `<option>${b.name}</option>`).join("");
    $("#rotFrom").innerHTML = opts;
    $("#rotTo").innerHTML = opts;
    $("#rotFrom").value = "80m";
    $("#rotTo").value = "6m";
  }
  renderRotation();
  $("#slots tbody").innerHTML = sched.slots.map((s, i) => `
    <tr data-i="${i}">
      <td><input class="hm" data-k="start" value="${esc(s.start)}" required pattern="([01][0-9]|2[0-3]):[0-5][0-9]|24:00" placeholder="HH:MM" maxlength="5"></td>
      <td><input class="hm" data-k="end" value="${esc(s.end)}" required pattern="([01][0-9]|2[0-3]):[0-5][0-9]|24:00" placeholder="HH:MM" maxlength="5"></td>
      <td><div class="chips">${bands.map((b) => {
        const n = s.bands.indexOf(b.name);
        return `<button type="button" class="chip ${n >= 0 ? "on" : ""}" data-band="${b.name}">${n >= 0 ? `<span class="ord">${n + 1}</span>` : ""}${b.name}</button>`;
      }).join("")}</div></td>
      <td><input type="number" min="1" max="30" data-k="cycles" value="${s.cycles || 1}"></td>
      <td><button type="button" class="ghost" data-del title="Remove slot">✕</button></td>
    </tr>`).join("") || `<tr><td colspan="5" class="muted">No slots: FlexWSPR stays on the default band.</td></tr>`;
  $("#schedDirty").textContent = schedDirty ? "Unsaved changes" : "";
  preview();
}

function changed() {
  schedDirty = true;
  $("#schedDirty").textContent = "Unsaved changes";
  preview();
}

$$("input[name=mode]").forEach((r) => r.addEventListener("change", (e) => {
  sched.mode = e.target.value;
  if (sched.mode === "rotation" && !sched.rotation.bands.length) {
    sched.rotation.bands = bandRange("80m", "6m");
  }
  schedDirty = true;
  renderSchedule();
}));
function bandRange(from, to) {
  const names = bands.map((b) => b.name);
  let a = names.indexOf(from), b = names.indexOf(to);
  if (a > b) [a, b] = [b, a];
  return names.slice(a, b + 1);
}
$("#rotRange").addEventListener("click", () => {
  sched.rotation.bands = bandRange($("#rotFrom").value, $("#rotTo").value);
  schedDirty = true;
  renderSchedule();
});
$("#rotBands").addEventListener("click", (e) => {
  const chip = e.target.closest(".chip");
  if (!chip) return;
  toggleBand(sched.rotation.bands, chip.dataset.band);
  schedDirty = true;
  renderSchedule();
});
$("#rotCycles").addEventListener("input", (e) => {
  sched.rotation.cycles = Math.min(30, Math.max(1, parseInt(e.target.value, 10) || 1));
  renderRotation();
  changed();
});
$("#defBand").addEventListener("change", (e) => { defBand = e.target.value; changed(); });
$("#slots").addEventListener("input", (e) => {
  const tr = e.target.closest("tr[data-i]");
  if (!tr || !e.target.dataset.k) return;
  const s = sched.slots[tr.dataset.i];
  s[e.target.dataset.k] = e.target.dataset.k === "cycles" ? Math.max(1, parseInt(e.target.value, 10) || 1) : e.target.value;
  changed();
});
$("#slots").addEventListener("click", (e) => {
  const tr = e.target.closest("tr[data-i]");
  if (!tr) return;
  const s = sched.slots[tr.dataset.i];
  const chip = e.target.closest(".chip");
  if (chip) {
    const b = chip.dataset.band;
    const n = s.bands.indexOf(b);
    if (n >= 0) s.bands.splice(n, 1); else s.bands.push(b);
    schedDirty = true;
    renderSchedule();
  } else if (e.target.closest("[data-del]")) {
    sched.slots.splice(tr.dataset.i, 1);
    schedDirty = true;
    renderSchedule();
  }
});
$("#addSlot").addEventListener("click", () => {
  const last = sched.slots.at(-1);
  sched.slots.push({ start: last ? last.end : "00:00", end: last ? last.start : "12:00", bands: [defBand], cycles: 1 });
  schedDirty = true;
  renderSchedule();
});

function schedConfig() {
  const c = clone(cfg);
  c.schedule = clone(sched);
  c.band = defBand;
  return c;
}

let previewTimer;
function preview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(async () => {
    try {
      const plan = await api("/api/plan?n=30", schedConfig());
      $("#planErr").textContent = "";
      $("#plan").innerHTML = plan.map((p) =>
        `<div class="${p.slot ? "" : "dflt"}" title="${p.slot ? "slot " + p.slot : "default band"}"><span class="muted">${hhmm(p.start)}</span><b>${esc(p.band)}</b></div>`).join("");
    } catch (e) {
      $("#planErr").textContent = e.message;
    }
  }, 250);
}

$("#saveSched").dataset.label = $("#saveSched").textContent;
$("#saveSched").addEventListener("click", async () => {
  if (sched.mode === "slots" && !$$("#slots input").every((i) => i.reportValidity())) return;
  if (await save(schedConfig(), $("#planErr"), $("#saveSched"))) $("#schedDirty").textContent = "";
});

// ---------- start ----------
(async function init() {
  try {
    bands = await api("/api/bands");
    const j = await api("/api/config");
    cfg = j.config;
    local = j.local;
  } catch (e) {
    $("#banner").textContent = "Cannot reach FlexWSPR: " + e.message;
    $("#banner").hidden = false;
    return;
  }
  await pollStatus();
  fillSettings();
  loadSchedule();
  let tab = "spots";
  try { tab = localStorage.getItem("tab") || tab; } catch {}
  if (status && status.state === "setup") tab = "settings";
  showTab(tab);
  setInterval(pollStatus, 2000);
  setInterval(pollLog, 2000);
  setInterval(pollOpenings, 60000);
})();
