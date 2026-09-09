/* ============================================================
   琳萌 Linmeng · web/static/app.js（浅色桌面端 v0.1）
   职责：登录/登出、HTTP 轮询快照、模块存在性导航、指标渲染、
        4 点短期曲线（迷你 + 主曲线）、明细行排版、底部状态栏。
   数据契约见 docs/api-reference.md 与 docs/architecture.md#数据模型。
   ============================================================ */
(function () {
  'use strict';

  /* ---------- 0. 配置（服务端注入；失败回退默认） ---------- */
  var CONFIG = {
    refresh_interval_seconds: 2, history_points: 4, auth_enabled: true,
    enable_cpu: true, enable_memory: true, enable_disk: true,
    enable_network: true, enable_host: true, enable_gpu: true,
    enable_proc: true, enable_fs: true
  };
  try {
    var parsed = JSON.parse(window.__LM_CONFIG_RAW__ || 'null');
    if (parsed) { CONFIG = parsed; }
    ['enable_cpu', 'enable_memory', 'enable_disk', 'enable_network', 'enable_host',
     'enable_gpu', 'enable_proc', 'enable_fs']
      .forEach(function (k) { if (typeof CONFIG[k] !== 'boolean') { CONFIG[k] = true; } });
  } catch (e) { /* 保持默认 */ }
  var POLL_MS = (CONFIG.refresh_interval_seconds > 0 ? CONFIG.refresh_interval_seconds : 2) * 1000;
  var HISTORY = CONFIG.history_points > 0 ? CONFIG.history_points : 4;

  /* ---------- 1. 元素引用 ---------- */
  var $ = function (id) { return document.getElementById(id); };
  var els = {
    dash: $('dash'), login: $('loginView'),
    topMeta: $('topMeta'), topRefresh: $('topRefresh'), btnLogout: $('btnLogout'),
    btnChangePw: $('btnChangePw'),
    btnTheme: $('btnTheme'),
    btnConfig: $('btnConfig'),
    pwModal: $('pwModal'), pwOld: $('pwOld'), pwNew: $('pwNew'),
    pwNew2: $('pwNew2'), pwErr: $('pwErr'), pwSave: $('pwSave'), pwCancel: $('pwCancel'),
    cfgModal: $('cfgModal'), cfgSeconds: $('cfgSeconds'), cfgHistory: $('cfgHistory'),
    cfgErr: $('cfgErr'), cfgSave: $('cfgSave'), cfgCancel: $('cfgCancel'),
    cfgCpu: $('cfgCpu'), cfgMemory: $('cfgMemory'), cfgDisk: $('cfgDisk'),
    cfgNetwork: $('cfgNetwork'), cfgHost: $('cfgHost'), cfgGpu: $('cfgGpu'),
    cfgProc: $('cfgProc'), cfgFs: $('cfgFs'),
    navItems: $('navItems'),
    panelName: $('panelName'), panelHint: $('panelHint'),
    kpiValue: $('kpiValue'), kpiUnit: $('kpiUnit'), kpiSub: $('kpiSub'),
    kpiBarWrap: $('kpiBarWrap'), kpiBar: $('kpiBar'), kpiBarPct: $('kpiBarPct'),
    subRow: $('subRow'), curveArea: $('curveArea'), mainSvg: $('mainSvg'),
    curveLegend: $('curveLegend'), legendA: $('legendA'), legendB: $('legendB'),
    legendBItem: $('legendBItem'), curvePlaceholder: $('curvePlaceholder'),
    specToggle: $('specToggle'), specBody: $('specBody'), specRows: $('specRows'),
    statusModule: $('statusModule'), statusRefresh: $('statusRefresh'),
    statusState: $('statusState'), statusRight: $('statusRight'),
    loginForm: $('loginForm'), loginPassword: $('loginPassword'),
    loginError: $('loginError'), loginBtn: $('loginBtn')
  };
  var panelEl = document.querySelector('.lm-panel');
  var emptyEl = $('emptyState');

  /* ---------- 2.0 主题（两态：亮色 / 暗色；按钮显示当前色，点击切换） ---------- */
  var THEME_LABEL = { light: '亮色', dark: '暗色' };
  var THEME_KEY = 'linmeng.theme';

  function themeMode() {
    return document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
  }

  function themeLabel(mode) {
    return THEME_LABEL[mode] || THEME_LABEL.light;
  }

  function applyThemeMode(mode) {
    document.documentElement.setAttribute('data-theme', mode);
    els.btnTheme.textContent = themeLabel(mode);
    els.statusRight.textContent = '主题：' + themeLabel(mode) + ' · 桌面端';
  }
  applyThemeMode(themeMode());   // 同步按钮与状态栏文案（html 首帧已按存储应用）

  /* ---------- 2. 格式化工具 ---------- */
  function isNum(v) { return typeof v === 'number' && isFinite(v); }
  function one(v) { return isNum(v) ? v.toFixed(1) : '-'; }
  function int(v) { return isNum(v) ? String(Math.round(v)) : '-'; }

  /* 大数计数显示：整数保持原样（≤7 位），过大时按 1000 进制配 K/M/G/T/P/E 单位，
     数值部分不超过 7 位（含小数），用于 fs 上限/inode 等可能超长的大计数（Q-02）。 */
  function fmtBigInt(n) {
    if (!isNum(n) || n < 0) { return '-'; }
    // 6 位及以下保留精确整数值（≤7 位要求内）；更大再进位配单位。
    if (n < 1000000) { return int(n); }
    var units = ['', 'K', 'M', 'G', 'T', 'P', 'E'];
    var v = n, i = 0;
    while (v >= 1000 && i < units.length - 1) { v /= 1000; i++; }
    if (v >= 100) { return v.toFixed(0) + ' ' + units[i]; }
    if (v >= 10) { return v.toFixed(1) + ' ' + units[i]; }
    return v.toFixed(2) + ' ' + units[i];
  }

  function fmtBytes(n) {
    if (!isNum(n) || n < 0) { return '-'; }
    var units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    var v = n, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? int(v) : v.toFixed(1)) + ' ' + units[i];
  }

  /* 返回 {n, u}：大数 + 独立单位，供 KPI 大字使用 */
  function splitBytes(n) {
    if (!isNum(n) || n < 0) { return { n: '-', u: '' }; }
    var units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
    var v = n, i = 0;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return { n: i === 0 ? int(v) : v.toFixed(1), u: units[i] };
  }

  function fmtRate(n) { var s = splitBytes(n); return s.n + ' ' + s.u + '/s'; }
  function pctText(v) { return isNum(v) ? v.toFixed(1) + '%' : '-'; }
  function freqText(mhz) { return isNum(mhz) ? int(mhz) + ' MHz' : '-'; }

  function fmtDur(sec) {
    if (!isNum(sec) || sec < 0) { return '-'; }
    var d = Math.floor(sec / 86400);
    var h = Math.floor((sec % 86400) / 3600);
    var m = Math.floor((sec % 3600) / 60);
    var s = Math.floor(sec % 60);
    var out = [];
    if (d > 0) { out.push(d + ' 天'); }
    if (h > 0) { out.push(h + ' 时'); }
    if (m > 0) { out.push(m + ' 分'); }
    if (s > 0 || out.length === 0) { out.push(s + ' 秒'); }
    return out.join(' ');
  }

  function loadText(a) {
    if (!a || !a.length) { return '-'; }
    return a.map(function (v) { return isNum(v) ? v.toFixed(2) : '-'; }).join(' / ');
  }

  function nowHMS() {
    return new Date().toLocaleTimeString('zh-CN', { hour12: false });
  }

  /* ---------- 3. 模块元数据 ---------- */
  /* snapKey: 快照字段名；color: 'a'=蓝 主序列 / 'b'=紫 次序列（design-system 映射） */
  var ORDER = ['host', 'cpu', 'memory', 'disk', 'network', 'gpu', 'proc', 'fs'];
  var DEFS = {
    host:    { name: '系统信息', snap: 'host',    color: null, hint: '静态元数据 · 不设曲线',        seriesA: null, seriesB: null, noCurve: true },
    cpu:     { name: 'CPU',     snap: 'cpu',     color: 'a', hint: '总使用率 · 请求间差分 · Y轴0-100', seriesA: 'cpu.percent', seriesB: null },
    memory:  { name: '内存',    snap: 'memory',  color: 'b', hint: '内存使用率（不含可回收缓存）',    seriesA: 'memory.percent', seriesB: null },
    disk:    { name: '硬盘',    snap: 'disk',    color: 'a', hint: '逐盘 IO 调用率；根盘附空间',     seriesA: 'disk.io_percent', seriesB: null },
    network: { name: '网络',    snap: 'network', color: 'a', hint: '逐网卡速率；TCP 聚合',           seriesA: 'network.rx_rate', seriesB: 'network.tx_rate' },
    gpu:     { name: 'GPU',     snap: 'gpu',     color: 'b', hint: '平均使用率（尽力而为）',          seriesA: 'gpu.percent', seriesB: null },
    proc:    { name: '进程',    snap: 'proc',    color: null, hint: '可调度任务计数（含线程）· 不设曲线', seriesA: null, seriesB: null, noCurve: true },
    fs:      { name: '文件描述符', snap: 'fs',   color: null, hint: '文件描述符占用 · 不设曲线',       seriesA: null, seriesB: null, noCurve: true }
  };
  /* 数组模块：snap[key].<ARR_FIELDS[key]> 每元素生成一个左栏实例 */
  var ARR_FIELDS = { disk: 'disks', network: 'nets' };

  function isPctSeries(key) {
    return key === 'cpu' || key === 'memory' || key === 'disk' || key === 'gpu';
  }

  function pick(obj, path) {
    var parts = path.split('.');
    var cur = obj;
    for (var i = 0; i < parts.length; i++) {
      if (cur == null) { return null; }
      cur = cur[parts[i]];
    }
    return cur;
  }

  function moduleLabel(key, idx) {
    if (key === 'disk') { return '硬盘#' + idx; }
    if (key === 'network') { return '网络#' + idx; }
    return DEFS[key].name;
  }

  /* 取某模块实例对象：数组模块按下标取元素，其余取模块对象本身 */
  function moduleObj(snap, key, idx) {
    var base = snap && snap[DEFS[key].snap];
    var arr = ARR_FIELDS[key];
    if (arr) {
      var list = base && base[arr];
      return (list && list[idx]) || null;
    }
    return base || null;
  }

  /* 实例化列表：返回 [{key, idx}]，用于左栏渲染/遍历 */
  function moduleItems(snap) {
    var out = [];
    ORDER.forEach(function (k) {
      var base = snap && snap[DEFS[k].snap];
      if (!base) { return; }
      var arr = ARR_FIELDS[k];
      if (arr) {
        var list = base[arr];
        if (!list || !list.length) { return; }
        for (var i = 0; i < list.length; i++) { out.push({ key: k, idx: i }); }
      } else {
        out.push({ key: k, idx: 0 });
      }
    });
    return out;
  }

  function instanceSig(items) {
    return items.map(function (it) { return it.key + '#' + it.idx; }).join(',');
  }

  function instanceKey(key, idx) { return key + '#' + idx; }

  /* 序列缓冲 id：数组模块每个实例独立一条曲线 */
  function seriesBufferId(seriesPath, idx) {
    return seriesPath + '#' + idx;
  }

  /* 取某实例当前帧的序列值 */
  function seriesValueAt(snap, key, idx, seriesPath) {
    if (!seriesPath) { return null; }
    var o = moduleObj(snap, key, idx);
    if (!o) { return null; }
    return pick(o, seriesPath.split('.').slice(1).join('.'));
  }

  /* ---------- 4. 状态 ---------- */
  var state = {
    snap: null, active: null, activeIdx: 0, listSig: '', items: {},
    buffers: {},      // 'cpu.percent#0' -> [..]
    openSpec: {},     // 各模块 L3 展开状态（刷新/切回保持）
    timer: null, failCount: 0, authRedirected: false, pollStarted: false, inFlight: false
  };

  function pushPoint(seriesPath, v) {
    if (seriesPath == null) { return; }
    var arr = state.buffers[seriesPath] || (state.buffers[seriesPath] = []);
    if (isNum(v)) { arr.push(v); } else { arr.push(null); }
    if (arr.length > HISTORY) { arr.shift(); }
  }

  function seriesValues(seriesPath) {
    var arr = state.buffers[seriesPath];
    return (arr || []).filter(function (v) { return isNum(v); });
  }

  /* ---------- 5. 曲线绘制（视图坐标：0..100 x 0..40） ---------- */
  var VB_W = 100, VB_H = 40, VB_PAD = 2;
  var SPARK_W = 28, SPARK_H = 14;

  function scaleRange(values, fixedHundred) {
    if (fixedHundred) { return { lo: 0, hi: 100 }; }
    var lo = Infinity, hi = -Infinity;
    for (var i = 0; i < values.length; i++) {
      if (values[i] < lo) { lo = values[i]; }
      if (values[i] > hi) { hi = values[i]; }
    }
    if (!isFinite(lo) || !isFinite(hi)) { return { lo: 0, hi: 1 }; }
    if (hi === lo) { hi = lo + Math.max(Math.abs(lo) * 0.1, 1); }
    var pad = (hi - lo) * 0.1;
    lo -= pad; hi += pad;
    if (lo < 0 && values.some(function (v) { return v >= 0; })) { lo = Math.max(lo, 0); }
    return { lo: lo, hi: hi };
  }

  function polyPoints(values, lo, hi, W, H) {
    var n = values.length;
    if (n === 0) { return ''; }
    var pts = [];
    for (var i = 0; i < n; i++) {
      var x = (i / Math.max(n - 1, 1)) * W;
      var span = (hi - lo) || 1;
      var y = H - ((values[i] - lo) / span) * (H - VB_PAD * 2) - VB_PAD;
      pts.push(x.toFixed(2) + ',' + y.toFixed(2));
    }
    return pts.join(' ');
  }

  function clearEl(el) { while (el.firstChild) { el.removeChild(el.firstChild); } }

  function makeEl(tag, cls, text) {
    var n = document.createElementNS ? document.createElementNS('http://www.w3.org/2000/svg', tag) : document.createElement(tag);
    if (cls) { n.setAttribute('class', cls); }
    if (text !== undefined) { n.textContent = text; }
    return n;
  }

  /* 主曲线绘制（当前激活实例） */
  function drawMain() {
    clearEl(els.mainSvg);
    var key = state.active;
    var def = DEFS[key];
    if (!def || def.noCurve) { return; }
    var idx = state.activeIdx || 0;
    var fixed = isPctSeries(key);
    var a = seriesValues(seriesBufferId(def.seriesA, idx));
    var b = def.seriesB ? seriesValues(seriesBufferId(def.seriesB, idx)) : [];
    if (!a.length && !b.length) { return; }

    var lo, hi;
    if (fixed) { lo = 0; hi = 100; }
    else {
      var r = scaleRange(a.concat(b), false);
      lo = r.lo; hi = r.hi;
    }

    if (a.length >= 2) {
      var ptsA = polyPoints(a, lo, hi, VB_W, VB_H);
      var area = makeEl('polygon', 'l-chart__area');
      area.setAttribute('points', ptsA + ' 100,40 0,40');
      els.mainSvg.appendChild(area);
      var lineA = makeEl('polyline', 'l-chart l-chart--a');
      lineA.setAttribute('points', ptsA);
      els.mainSvg.appendChild(lineA);
    }
    if (b.length >= 2) {
      var lineB = makeEl('polyline', 'l-chart l-chart--b');
      lineB.setAttribute('points', polyPoints(b, lo, hi, VB_W, VB_H));
      els.mainSvg.appendChild(lineB);
    }

    if (a.length < 2 && b.length < 2) {
      els.curvePlaceholder.hidden = false;
      els.curvePlaceholder.textContent = '采集中…（≥2 帧后绘制曲线）';
      els.curveLegend.hidden = true;
    } else {
      els.curvePlaceholder.hidden = true;
      els.curveLegend.hidden = false;
    }
  }

  function drawSpark(key, idx) {
    var it = state.items[instanceKey(key, idx)];
    if (!it || !it.svg) { return; }
    clearEl(it.svg);
    var def = DEFS[key];
    var fixed = isPctSeries(key);
    var a = seriesValues(seriesBufferId(def.seriesA, idx));
    var b = def.seriesB ? seriesValues(seriesBufferId(def.seriesB, idx)) : [];
    if (!a.length && !b.length) { return; }
    var lo, hi;
    if (fixed) { lo = 0; hi = 100; }
    else {
      var r = scaleRange(a.concat(b), false);
      lo = r.lo; hi = r.hi;
    }
    appendSparkLine(it.svg, a, lo, hi, 'spark-line');
    if (b.length) { appendSparkLine(it.svg, b, lo, hi, 'spark-line2'); }
  }

  function appendSparkLine(svg, values, lo, hi, cls) {
    if (values.length < 2) { return; }
    var pts = polyPoints(values, lo, hi, SPARK_W, SPARK_H);
    var p = makeEl('polyline', cls, null);
    p.setAttribute('points', pts);
    svg.appendChild(p);
  }

  /* ---------- 6. 导航与面板渲染 ---------- */
  function navMetric(key, idx, snap) {
    var o = moduleObj(snap, key, idx);
    if (!o) { return '-'; }
    switch (key) {
      case 'host': return o.hostname || '-';
      case 'cpu': return pctText(o.percent);
      case 'memory': return '已用 ' + fmtBytes(o.used) + ' / 总 ' + fmtBytes(o.total) + ' · ' + pctText(o.percent);
      case 'disk': return isNum(o.io_percent) ? pctText(o.io_percent) : '-';
      case 'network': return '↓ ' + fmtRate(o.rx_rate) + ' · ↑ ' + fmtRate(o.tx_rate);
      case 'gpu': return pctText(o.percent);
      case 'proc': return int(o.total);
      case 'fs': return fmtBigInt(o.file_descriptors) + ' / ' + fmtBigInt(o.file_desc_limit);
      default: return '-';
    }
  }

  function buildNav(snap) {
    var items = moduleItems(snap);
    var sig = instanceSig(items);
    if (sig === state.listSig) {
      items.forEach(function (it) { updateNavItem(it.key, it.idx, snap); });
      return;
    }
    state.listSig = sig;
    clearEl(els.navItems);
    state.items = {};
    if (items.length === 0) {
      state.active = null;
      state.activeIdx = 0;
      panelEl.classList.add('is-hidden');
      emptyEl.classList.add('is-visible');
      els.statusModule.textContent = '—';
      return;
    }
    panelEl.classList.remove('is-hidden');
    emptyEl.classList.remove('is-visible');

    items.forEach(function (it) {
      var key = it.key, idx = it.idx;
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'lm-nav__item';
      btn.setAttribute('data-module', key);
      btn.setAttribute('data-idx', String(idx));
      btn.setAttribute('aria-pressed', 'false');

      var text = document.createElement('span');
      text.className = 'lm-nav__text';
      var name = document.createElement('span');
      name.className = 'lm-nav__name';
      name.textContent = moduleLabel(key, idx);
      var metric = document.createElement('span');
      metric.className = 'lm-nav__metric';
      metric.textContent = navMetric(key, idx, snap);
      text.appendChild(name);
      text.appendChild(metric);
      btn.appendChild(text);

      if (!DEFS[key].noCurve) {
        var spark = document.createElement('span');
        spark.className = 'lm-nav__spark';
        spark.setAttribute('aria-hidden', 'true');
        var svg = makeEl('svg', null, null);
        svg.setAttribute('viewBox', '0 0 28 14');
        svg.setAttribute('preserveAspectRatio', 'none');
        spark.appendChild(svg);
        btn.appendChild(spark);
      }
      btn.addEventListener('click', (function (k, i) {
        return function () { selectModule(k, i); };
      })(key, idx));

      els.navItems.appendChild(btn);
      state.items[instanceKey(key, idx)] = {
        btn: btn, metric: metric, svg: spark ? spark.querySelector('svg') : null
      };
    });

    // 保持当前选择（key+idx 仍存在），否则默认第一个实例
    if (!state.active || !state.items[instanceKey(state.active, state.activeIdx || 0)]) {
      state.active = items[0].key;
      state.activeIdx = items[0].idx;
    }
    refreshActiveState();
    renderActive(snap);
    items.forEach(function (it) { drawSpark(it.key, it.idx); });
  }

  function updateNavItem(key, idx, snap) {
    var item = state.items[instanceKey(key, idx)];
    if (item && item.metric) { item.metric.textContent = navMetric(key, idx, snap); }
    drawSpark(key, idx);
  }

  function refreshActiveState() {
    var curKey = instanceKey(state.active, state.activeIdx || 0);
    Object.keys(state.items).forEach(function (k) {
      var on = (k === curKey);
      state.items[k].btn.classList.toggle('is-active', on);
      state.items[k].btn.setAttribute('aria-pressed', String(on));
    });
  }

  function selectModule(key, idx) {
    var k = key || state.active;
    var i = (idx === undefined || idx === null) ? 0 : idx;
    if (!state.items[instanceKey(k, i)]) { return; }
    state.active = k;
    state.activeIdx = i;
    refreshActiveState();
    renderActive(state.snap);
  }

  function setBar(pct) {
    if (!isNum(pct)) {
      els.kpiBarWrap.hidden = true;
      return;
    }
    pct = Math.max(0, Math.min(100, pct));
    els.kpiBarWrap.hidden = false;
    els.kpiBar.style.width = pct + '%';
    els.kpiBarPct.textContent = pct.toFixed(1) + '%';
    els.kpiBar.classList.toggle('is-warn', pct >= 70 && pct < 90);
    els.kpiBar.classList.toggle('is-danger', pct >= 90);
    els.kpiValue.classList.toggle('is-warn', pct >= 70 && pct < 90);
    els.kpiValue.classList.toggle('is-danger', pct >= 90);
  }

  function renderSub(items) {
    clearEl(els.subRow);
    items.forEach(function (it) {
      if (!it) { return; }
      var wrap = document.createElement('span');
      wrap.className = 'lm-sub__item';
      if (it[0]) {
        var lb = document.createElement('span');
        lb.className = 'lm-sub__label';
        lb.textContent = it[0] + '：';
        wrap.appendChild(lb);
      }
      var v = document.createElement('span');
      v.className = 'lm-sub__value';
      v.textContent = it[1];
      wrap.appendChild(v);
      els.subRow.appendChild(wrap);
    });
  }

  function renderDetailRows(rows) {
    clearEl(els.specRows);
    if (!rows || rows.length === 0) {
      rows = [['规格明细', '暂无数据']];
    }
    rows.forEach(function (pair) {
      var row = document.createElement('div');
      row.className = 'lm-detailrow';
      var label = document.createElement('span');
      label.className = 'lm-detailrow__label';
      label.textContent = pair[0];
      var value = document.createElement('span');
      value.className = 'lm-detailrow__value';
      value.textContent = pair[1];
      row.appendChild(label);
      row.appendChild(value);
      els.specRows.appendChild(row);
    });
  }

  /* 规格区首次进入的默认展开策略：
     CPU 逻辑核 >16（不含）时默认收起 L3（避免首屏信息过密，Q-04）；其余模块默认展开。 */
  function specDefaultOpen(key) {
    if (key === 'cpu') {
      var pc = state.snap && state.snap.cpu && state.snap.cpu.per_core;
      if (pc && pc.length > 16) { return false; }
    }
    return true;
  }

  /* 折叠规格区：无曲线模块（系统信息/进程/文件描述符）无折叠；
     其余模块首次按 specDefaultOpen 决定，用户收起后按 state.openSpec 记忆（刷新/切回保持） */
  function specMode(openRows) {
    var key = state.active;
    var def = DEFS[key];
    var plain = !!(def && def.noCurve);
    var open = true;
    if (!plain) {
      if (!Object.prototype.hasOwnProperty.call(state.openSpec, key)) {
        state.openSpec[key] = specDefaultOpen(key);
      }
      open = state.openSpec[key];
    }
    renderDetailRows(openRows);
    if (plain) {
      els.specToggle.hidden = true;
      els.specBody.hidden = false;
      return;
    }
    els.specToggle.hidden = false;
    els.specToggle.textContent = open ? '收起规格（L3）' : '展开规格（L3）';
    els.specToggle.setAttribute('aria-expanded', String(open));
    els.specBody.hidden = !open;
  }

  function kpiLayout(valueText, unitText, subText) {
    els.kpiValue.textContent = valueText;
    els.kpiUnit.textContent = unitText;
    els.kpiSub.textContent = subText;
  }

  function renderActive(snap) {
    if (!snap || !state.active) { return; }
    var key = state.active;
    var def = DEFS[key];
    var idx = state.activeIdx || 0;
    var label = moduleLabel(key, idx);
    els.panelName.textContent = label;
    els.panelHint.textContent = def.hint + (key === 'disk' || key === 'network' ? ' · ' + label : '');
    els.statusModule.textContent = label;

    /* KPI 前清空阈值样式 */
    els.kpiValue.classList.remove('is-warn', 'is-danger');
    els.kpiBar.classList.remove('is-warn', 'is-danger');

    switch (key) {
      case 'host': {
        var h = snap.host;
        kpiLayout(h.hostname || '-', '', h.os || '-');
        setBar(null);
        renderSub([['操作系统', h.os || '-'], ['内核版本', h.kernel || '-'],
                   ['运行时长', fmtDur(h.uptime_seconds)], ['局域网 IP', h.lan_ip || '-']]);
        showMetaCurve();
        specMode([
          ['主机名', h.hostname || '-'],
          ['操作系统', h.os || '-'],
          ['内核版本', h.kernel || '-'],
          ['运行时长', fmtDur(h.uptime_seconds)],
          ['局域网 IP', h.lan_ip || '-']
        ]);
        break;
      }
      case 'cpu': {
        var c = snap.cpu;
        var p = isNum(c.percent) ? c.percent : null;
        kpiLayout(p == null ? '-' : p.toFixed(1), '%', c.frequency != null ? freqText(c.frequency) : '频率 -');
        setBar(p);
        renderSub([['负载 1/5/15', loadText(c.load_avg)],
                   ['逻辑核', c.per_core ? c.per_core.length : '-'],
                   ['频率', freqText(c.frequency)]]);
        showChartCurve(['总使用率'], c.frequency != null ? freqText(c.frequency) : null);
        var rows = [['总使用率', pctText(c.percent)],
                    ['负载(1/5/15)', loadText(c.load_avg)],
                    ['逻辑核数', c.per_core ? int(c.per_core.length) : '-'],
                    ['当前频率', freqText(c.frequency)]];
        if (c.per_core) {
          for (var i = 0; i < c.per_core.length; i++) {
            rows.push(['核心 ' + i, pctText(c.per_core[i])]);
          }
        }
        specMode(rows);
        break;
      }
      case 'memory': {
        var m = snap.memory;
        kpiLayout(isNum(m.percent) ? m.percent.toFixed(1) : '-', '%', '已用 ' + fmtBytes(m.used));
        setBar(m.percent);
        renderSub([['已用 / 总量', fmtBytes(m.used) + ' / ' + fmtBytes(m.total)],
                   ['可用', fmtBytes(m.available)],
                   ['交换分区', fmtBytes(m.swap.used) + ' / ' + fmtBytes(m.swap.total)]]);
        showChartCurve(['使用率'], null);
        specMode([
          ['物理内存总量', fmtBytes(m.total)],
          ['已用', fmtBytes(m.used)],
          ['使用率', pctText(m.percent)],
          ['可用', fmtBytes(m.available)],
          ['Buffer', fmtBytes(m.buff)],
          ['Cache', fmtBytes(m.cache)],
          ['交换 总量', fmtBytes(m.swap.total)],
          ['交换 已用', fmtBytes(m.swap.used)],
          ['交换 使用率', pctText(m.swap.percent)]
        ]);
        break;
      }
      case 'disk': {
        var d = moduleObj(snap, 'disk', idx);
        if (!d) { break; }
        var isRoot = (d.mount === '/');
        var io = isNum(d.io_percent) ? d.io_percent : null;
        var spPct = isNum(d.percent) ? pctText(d.percent) : '-';
        var kpiSub = '设备 ' + (d.name || '-');
        if (isRoot) {
          kpiSub += ' · 空间 已用 ' + fmtBytes(d.used) + ' / ' + fmtBytes(d.total) + '（' + spPct + '）';
        }
        kpiLayout(io == null ? '-' : io.toFixed(1), '%', kpiSub);
        setBar(io);
        var subRows = [['设备', d.name || '-']];
        var specRows = [
          ['IO 调用占用率', io == null ? '-' : pctText(io)],
          ['读吞吐', isNum(d.read_rate) ? fmtRate(d.read_rate) : '-'],
          ['写吞吐', isNum(d.write_rate) ? fmtRate(d.write_rate) : '-'],
          ['读 IOPS', isNum(d.read_iops) ? d.read_iops.toFixed(1) + ' ops/s' : '-'],
          ['写 IOPS', isNum(d.write_iops) ? d.write_iops.toFixed(1) + ' ops/s' : '-']
        ];
        if (isRoot) {
          subRows.push(['空间使用率', pctText(d.percent)],
                       ['已用 / 总量', fmtBytes(d.used) + ' / ' + fmtBytes(d.total)],
                       ['inode 使用率', pctText(d.inodes_percent)],
                       ['挂载点', '/']);
          specRows.push(['空间使用率', pctText(d.percent)],
                        ['分区总量', fmtBytes(d.total)],
                        ['已用', fmtBytes(d.used)],
                        ['inode 已用', fmtBigInt(d.inodes)],
                        ['inode 使用率', pctText(d.inodes_percent)],
                        ['挂载点', '/']);
        } else {
          subRows.push(['挂载点', '—（非根分区）']);
          specRows.push(['挂载点', '—（非根分区）']);
        }
        renderSub(subRows);
        showChartCurve(['IO 调用率'], null);
        specMode(specRows);
        break;
      }
      case 'network': {
        var netMod = snap.network;
        var n = moduleObj(snap, 'network', idx);
        if (!n) { break; }
        var rx = splitBytes(n.rx_rate);
        var rxTxt = rx.n, rxUnit = rx.u + '/s';
        kpiLayout(rxTxt, rxUnit, '网卡 ' + (n.name || '-'));
        setBar(null);
        var netSub = [['网卡', n.name || '-'],
                      ['下行', fmtRate(n.rx_rate)],
                      ['上行', fmtRate(n.tx_rate)],
                      ['累计收 / 发', fmtBytes(n.rx_bytes) + ' / ' + fmtBytes(n.tx_bytes)]];
        if (idx === 0) {
          netSub.push(['TCP 已建连（聚合）', int(netMod.tcp_established)]);
        }
        renderSub(netSub);
        showChartCurve(['下行(蓝)', '上行(紫)'], null);
        var netRows = [
          ['网卡', n.name || '-'],
          ['累计接收', fmtBytes(n.rx_bytes)],
          ['累计发送', fmtBytes(n.tx_bytes)],
          ['下行速率', fmtRate(n.rx_rate)],
          ['上行速率', fmtRate(n.tx_rate)],
          ['接收丢包', int(n.rx_drops)],
          ['发送丢包', int(n.tx_drops)],
          ['接收错误', int(n.rx_errors)],
          ['发送错误', int(n.tx_errors)]
        ];
        if (idx === 0) {
          netRows.push(['TCP 已建连接（聚合）', int(netMod.tcp_established)]);
        }
        specMode(netRows);
        break;
      }
      case 'gpu': {
        var g = snap.gpu;
        kpiLayout(isNum(g.percent) ? g.percent.toFixed(1) : '-', '%',
                  (g.name && g.name[0]) || '-');
        setBar(g.percent);
        renderSub([['GPU 数量', int(g.count)], ['整体使用率', pctText(g.percent)]]);
        showChartCurve(['平均使用率'], null);
        var gRows = [['GPU 数量', int(g.count)], ['整体使用率', pctText(g.percent)]];
        if (g.per_gpu && g.per_gpu.length) {
          g.per_gpu.forEach(function (card) {
            gRows.push(['GPU ' + card.index, (card.name || '-') + ' · ' + pctText(card.percent)]);
          });
        } else if (g.name && g.name.length) {
          g.name.forEach(function (nm, i) { gRows.push(['GPU ' + i, nm + ' · ' + pctText(isNum(g.percent) ? g.percent : null)]); });
        }
        specMode(gRows);
        break;
      }
      case 'proc': {
        var pp = snap.proc;
        kpiLayout(int(pp.total), '', '运行中 ' + int(pp.running));
        setBar(null);
        renderSub([['运行态进程', int(pp.running)], ['进程总数', int(pp.total)]]);
        showMetaCurve();
        specMode([
          ['进程总数（task 口径，含线程）', int(pp.total)],
          ['运行态进程数', int(pp.running)]
        ]);
        break;
      }
      case 'fs': {
        var ff = snap.fs;
        var fdUsed = isNum(ff.file_descriptors) ? ff.file_descriptors : 0;
        var fdLim = isNum(ff.file_desc_limit) ? ff.file_desc_limit : 0;
        kpiLayout(fmtBigInt(fdUsed), '', '上限 ' + fmtBigInt(fdLim));
        if (fdLim > 0) {
          var fpct = Math.min(100, fdUsed / fdLim * 100);
          els.kpiBarWrap.hidden = false;
          els.kpiBar.style.width = fpct + '%';
          els.kpiBarPct.textContent = fpct.toFixed(1) + '%';
          els.kpiBar.classList.toggle('is-warn', fpct >= 70 && fpct < 90);
          els.kpiBar.classList.toggle('is-danger', fpct >= 90);
        } else {
          els.kpiBarWrap.hidden = true;
        }
        renderSub([['已用', fmtBigInt(fdUsed)], ['上限', fmtBigInt(fdLim)]]);
        showMetaCurve();
        specMode([
          ['已分配文件描述符', fmtBigInt(fdUsed)],
          ['文件描述符上限（fs.file-max）', fmtBigInt(fdLim)]
        ]);
        break;
      }
      default:
        kpiLayout('-', '', '');
        setBar(null);
        renderSub([]);
        els.specBody.hidden = true;
    }

    /* 曲线绘图 */
    drawMain();
  }

  /* 主曲线区两种形态 */
  function showMetaCurve() {
    els.curveArea.classList.add('lm-curve--meta');
    els.mainSvg.hidden = true;
    els.curveLegend.hidden = true;
    els.curvePlaceholder.hidden = false;
    els.curvePlaceholder.textContent = '静态数据 · 系统信息不设曲线';
  }

  function showChartCurve(labels, subNote) {
    els.curveArea.classList.remove('lm-curve--meta');
    els.mainSvg.hidden = false;
    els.curveLegend.hidden = false;
    els.legendA.textContent = labels[0];
    els.legendBItem.hidden = !(labels[1]);
    els.legendB.textContent = labels[1] || '';
    els.curvePlaceholder.hidden = true;
  }

  /* ---------- 7. 轮询 ---------- */
  function setState(text, isErr) {
    els.statusState.textContent = text;
    els.statusState.classList.toggle('lm-statusbar__state-err', !!isErr);
  }

  function schedule() {
    if (!state.pollStarted) { return; }
    state.timer = setTimeout(poll, POLL_MS);
  }

  function handleInfoOK(snap) {
    state.failCount = 0;
    state.snap = snap;
    setState('轮询正常', false);
    var t = nowHMS();
    els.topRefresh.textContent = t;
    els.statusRefresh.textContent = '最近刷新 ' + t;

    if (snap.host) {
      els.topMeta.textContent = (snap.host.hostname || '-') + ' · ' + (snap.host.lan_ip || '-');
    }
    /* 追加曲线点（仅成功帧）：按实例遍历，每个实例独立缓冲 */
    moduleItems(snap).forEach(function (it) {
      var def = DEFS[it.key];
      if (def.seriesA) { pushPoint(seriesBufferId(def.seriesA, it.idx), seriesValueAt(snap, it.key, it.idx, def.seriesA)); }
      if (def.seriesB) { pushPoint(seriesBufferId(def.seriesB, it.idx), seriesValueAt(snap, it.key, it.idx, def.seriesB)); }
    });
    buildNav(snap);
    renderActive(snap);
    schedule();
  }

  function handleInfoFail(msg) {
    state.failCount++;
    if (state.failCount >= 3) {
      setState('连接异常 · ' + state.failCount + ' 次失败（保留上帧数据）', true);
    } else {
      setState('请求失败，稍后重试（保留上帧数据）', true);
    }
    els.statusRefresh.textContent = '最近刷新 ' + nowHMS();
    schedule();
  }

  function poll() {
    /* 互斥：避免“保存配置后立即轮询”与在途请求重叠产生双定时器 */
    if (!state.pollStarted || state.inFlight) { return; }
    state.inFlight = true;
    fetch('/api/system/info', { credentials: 'same-origin' }).then(function (resp) {
      if (resp.status === 401) {
        if (CONFIG.auth_enabled && !state.authRedirected) {
          state.authRedirected = true;
          window.location.href = '/login';
        }
        return null;
      }
      if (!resp.ok) { handleInfoFail('HTTP ' + resp.status); return null; }
      return resp.json();
    }).then(function (snap) {
      if (snap) { handleInfoOK(snap); }
    }).catch(function () {
      if (state.pollStarted) { handleInfoFail('网络'); }
    }).then(function () {
      state.inFlight = false;
    });
  }

  function startPoll() {
    if (state.pollStarted) { return; }
    state.pollStarted = true;
    poll();
  }

  function stopPoll() {
    state.pollStarted = false;
    if (state.timer) { clearTimeout(state.timer); state.timer = null; }
  }

  /* ---------- 8. 视图切换与登录 ---------- */
  function showDash() {
    els.login.hidden = true;
    els.dash.hidden = false;
    els.btnLogout.hidden = !CONFIG.auth_enabled;
    els.btnTheme.hidden = false;
    els.btnChangePw.hidden = !CONFIG.auth_enabled;
    els.btnConfig.hidden = false;
  }

  function showLoginView() {
    els.dash.hidden = true;
    els.login.hidden = false;
    els.btnLogout.hidden = true;
    els.btnTheme.hidden = true;
    els.btnChangePw.hidden = true;
    els.btnConfig.hidden = true;
    els.loginError.textContent = '';
    els.loginPassword.value = '';
    els.pwModal.hidden = true;
    els.cfgModal.hidden = true;
  }

  function openPwModal() {
    els.pwModal.hidden = false;
    els.pwErr.textContent = '';
    els.pwOld.value = '';
    els.pwNew.value = '';
    els.pwNew2.value = '';
  }

  function closePwModal() {
    els.pwModal.hidden = true;
  }

  /* —— “修改配置”弹窗：刷新间隔 / 曲线点数 / 模块开关（8 项） —— */
  function cfgToggleList() {
    return [
      ['enable_cpu', els.cfgCpu], ['enable_memory', els.cfgMemory],
      ['enable_disk', els.cfgDisk], ['enable_network', els.cfgNetwork],
      ['enable_host', els.cfgHost], ['enable_gpu', els.cfgGpu],
      ['enable_proc', els.cfgProc], ['enable_fs', els.cfgFs]
    ];
  }

  function openConfigModal() {
    els.cfgSeconds.value = String(CONFIG.refresh_interval_seconds || 4);
    els.cfgHistory.value = String(CONFIG.history_points || 4);
    cfgToggleList().forEach(function (pair) {
      pair[1].checked = !!CONFIG[pair[0]];
    });
    els.cfgErr.textContent = '';
    els.cfgModal.hidden = false;
  }

  function closeConfigModal() {
    els.cfgModal.hidden = true;
  }

  function validInt(text, lo, hi) {
    var n = Number(text);
    if (String(text).trim() === '' || !isFinite(n) || Math.floor(n) !== n || n < lo || n > hi) {
      return null;
    }
    return n;
  }

  function route() {
    var isLogin = /\/login/.test(window.location.pathname);
    if (!CONFIG.auth_enabled) {
      if (isLogin) { window.location.replace('/'); return; }
      showDash();
      startPoll();
      return;
    }
    if (isLogin) {
      stopPoll();
      showLoginView();
    } else {
      showDash();
      startPoll();
    }
  }

  els.btnLogout.addEventListener('click', function () {
    fetch('/api/auth/logout', { method: 'POST', credentials: 'same-origin' })
      .catch(function () { /* 忽略 */ })
      .then(function () { window.location.href = '/login'; });
  });

  /* 主题按钮：显示当前色（亮色/暗色），点击切换为另一种 */
  els.btnTheme.addEventListener('click', function () {
    var next = themeMode() === 'light' ? 'dark' : 'light';
    applyThemeMode(next);
    try { localStorage.setItem(THEME_KEY, next); } catch (e) { /* 忽略 */ }
  });

  els.btnChangePw.addEventListener('click', openPwModal);
  els.pwCancel.addEventListener('click', closePwModal);
  els.pwModal.addEventListener('click', function (ev) {
    if (ev.target === els.pwModal) { closePwModal(); }
  });

  els.pwSave.addEventListener('click', function () {
    if (els.pwSave.disabled) { return; }
    var oldPw = els.pwOld.value;
    var newPw = els.pwNew.value;
    var newPw2 = els.pwNew2.value;
    els.pwErr.textContent = '';
    if (!oldPw) { els.pwErr.textContent = '请输入原密码'; return; }
    if (!newPw) { els.pwErr.textContent = '请输入新密码'; return; }
    if (newPw !== newPw2) { els.pwErr.textContent = '两次输入的新密码不一致'; return; }
    if (newPw === oldPw) { els.pwErr.textContent = '新密码不能与原密码相同'; return; }

    els.pwSave.disabled = true;
    fetch('/api/auth/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ old_password: oldPw, new_password: newPw })
    }).then(function (resp) {
      if (resp.status === 200) {
        closePwModal();
        setState('密码已更新（旧密码立即失效）', false);
        return null;
      }
      if (resp.status === 401) { window.location.href = '/login'; return null; }
      return resp.json().then(function (b) {
        els.pwErr.textContent = (b && b.error) ? b.error : '修改失败（HTTP ' + resp.status + '）';
      });
    }).catch(function () {
      els.pwErr.textContent = '网络错误，请重试';
    }).then(function () {
      els.pwSave.disabled = false;
    });
  });

  els.btnConfig.addEventListener('click', openConfigModal);
  els.cfgCancel.addEventListener('click', closeConfigModal);
  els.cfgModal.addEventListener('click', function (ev) {
    if (ev.target === els.cfgModal) { closeConfigModal(); }
  });

  els.cfgSave.addEventListener('click', function () {
    if (els.cfgSave.disabled) { return; }
    var sec = validInt(els.cfgSeconds.value, 1, 5);
    var hist = validInt(els.cfgHistory.value, 2, 60);
    if (sec === null) { els.cfgErr.textContent = '刷新间隔请输入 1–5 的整数（秒）'; return; }
    if (hist === null) { els.cfgErr.textContent = '曲线点数请输入 2–60 的整数'; return; }

    var payload = {
      refresh_interval_seconds: sec,
      history_points: hist
    };
    cfgToggleList().forEach(function (pair) { payload[pair[0]] = pair[1].checked; });

    els.cfgSave.disabled = true;
    els.cfgErr.textContent = '';
    fetch('/api/settings', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(payload)
    }).then(function (resp) {
      if (resp.status === 200) {
        CONFIG.refresh_interval_seconds = sec;
        CONFIG.history_points = hist;
        cfgToggleList().forEach(function (pair) { CONFIG[pair[0]] = pair[1].checked; });
        POLL_MS = sec * 1000;                       // 刷新间隔即时生效
        if (HISTORY !== hist) {                     // 曲线点数即时生效并裁剪多余点
          HISTORY = hist;
          Object.keys(state.buffers).forEach(function (k) {
            if (state.buffers[k].length > HISTORY) {
              state.buffers[k] = state.buffers[k].slice(-HISTORY);
            }
          });
        }
        if (state.timer) { clearTimeout(state.timer); state.timer = null; }
        closeConfigModal();
        setState('配置已保存（写入 setting.json 并生效）', false);
        poll();   // 立即恢复轮询链（保存前被取消的下一次轮询不会自动续期，须在此重新发起）
        return null;
      }
      if (resp.status === 401) { window.location.href = '/login'; return null; }
      return resp.json().then(function (b) {
        els.cfgErr.textContent = (b && b.error) ? b.error : '保存失败（HTTP ' + resp.status + '）';
      });
    }).catch(function () {
      els.cfgErr.textContent = '网络错误，请重试';
    }).then(function () {
      els.cfgSave.disabled = false;
    });
  });

  els.specToggle.addEventListener('click', function () {
    var key = state.active;
    state.openSpec[key] = !state.openSpec[key];
    renderActive(state.snap);
  });

  els.loginForm.addEventListener('submit', function (ev) {
    ev.preventDefault();
    if (els.loginBtn.disabled) { return; }
    els.loginBtn.disabled = true;
    els.loginError.textContent = '';
    var password = els.loginPassword.value;
    fetch('/api/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ password: password })
    }).then(function (resp) {
      if (resp.status === 200) { window.location.href = '/'; return null; }
      return resp.json().then(function (b) {
        els.loginError.textContent = (b && b.error) ? b.error : '登录失败（HTTP ' + resp.status + '）';
      });
    }).catch(function () {
      els.loginError.textContent = '网络错误，请重试';
    }).then(function () {
      els.loginBtn.disabled = false;
    });
  });

  /* 惰性：明细行内容在展开/渲染时重建，无需额外监听 */

  /* 初次加载：默认选中首模块占位渲染 */
  els.panelName.textContent = '加载中…';
  route();
})();
