// Pulse status page: plain JS, no build step, no external dependencies.
// Talks to the API by a relative URL, so it works behind any host (nginx, Gateway API, port-forward).
'use strict';

(function () {
  const STATUS_URL = 'api/v1/status';
  const uptimeUrl = (id) => `api/v1/monitors/${encodeURIComponent(id)}/uptime?days=7`;
  const REFRESH_MS = 15000;
  const FETCH_TIMEOUT_MS = 10000;
  const CHART_HOURS = 7 * 24;
  const HOUR_MS = 3600 * 1000;

  const OVERALL_TEXT = {
    up: 'Все системы работают',
    degraded: 'Частичный сбой',
    down: 'Серьёзный сбой',
    unknown: 'Нет данных о проверках',
  };
  const STATUS_TEXT = { up: 'работает', down: 'недоступен', unknown: 'нет данных' };

  const $ = (id) => document.getElementById(id);
  const els = {
    overall: $('overall'),
    overallTitle: $('overall-title'),
    overallMeta: $('overall-meta'),
    error: $('error'),
    monitors: $('monitors'),
    monitorsEmpty: $('monitors-empty'),
    incidents: $('incidents'),
    incidentsEmpty: $('incidents-empty'),
    updated: $('updated'),
    tpl: $('monitor-tpl'),
  };

  let openMonitorId = null; // expanded monitor survives refreshes
  let lastUptime = null; // {id, items} for re-rendering the chart on resize
  let timer = null;

  // ---------- helpers ----------

  async function getJSON(url) {
    const ctrl = new AbortController();
    const t = setTimeout(() => ctrl.abort(), FETCH_TIMEOUT_MS);
    try {
      const res = await fetch(url, { headers: { Accept: 'application/json' }, cache: 'no-store', signal: ctrl.signal });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      return await res.json();
    } catch (e) {
      throw new Error(e.name === 'AbortError' ? 'таймаут запроса' : e.message);
    } finally {
      clearTimeout(t);
    }
  }

  function el(tag, cls, text) {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text !== undefined && text !== null) node.textContent = text;
    return node;
  }

  function pct(v, digits = 2) {
    if (v === null || v === undefined || Number.isNaN(v)) return '—';
    // 99.95% / 80% / 100%: no trailing zeros, but 99.999% is never rounded up to a fake 100%
    const factor = 10 ** digits;
    const p = Math.floor(v * 100 * factor) / factor;
    return `${Number(p.toFixed(digits))}%`;
  }

  function ago(iso) {
    if (!iso) return 'не проверялся';
    const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (s < 5) return 'только что';
    if (s < 60) return `${s} с назад`;
    if (s < 3600) return `${Math.floor(s / 60)} мин назад`;
    if (s < 86400) return `${Math.floor(s / 3600)} ч назад`;
    return `${Math.floor(s / 86400)} д назад`;
  }

  function duration(iso) {
    const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (s < 60) return `${s} с`;
    if (s < 3600) return `${Math.floor(s / 60)} мин`;
    if (s < 86400) return `${Math.floor(s / 3600)} ч ${Math.floor((s % 3600) / 60)} мин`;
    return `${Math.floor(s / 86400)} д ${Math.floor((s % 86400) / 3600)} ч`;
  }

  const localTime = (iso) => (iso ? new Date(iso).toLocaleString('ru-RU') : '');

  function showError(msg) {
    els.error.textContent = msg;
    els.error.hidden = !msg;
  }

  // ---------- status ----------

  function renderOverall(data) {
    const overall = OVERALL_TEXT[data.overall] ? data.overall : 'unknown';
    els.overall.className = `overall overall-${overall}`;
    els.overallTitle.textContent = OVERALL_TEXT[overall];
    els.overallMeta.textContent =
      `мониторов: ${data.monitors.length} · открытых инцидентов: ${data.open_incidents.length}`;
  }

  function renderMonitors(monitors) {
    els.monitorsEmpty.hidden = monitors.length > 0;
    const ids = new Set(monitors.map((m) => String(m.id)));
    if (openMonitorId !== null && !ids.has(openMonitorId)) openMonitorId = null;

    const frag = document.createDocumentFragment();
    for (const m of monitors) {
      const li = els.tpl.content.firstElementChild.cloneNode(true);
      const status = STATUS_TEXT[m.status] ? m.status : 'unknown';
      const id = String(m.id);
      li.classList.add(`s-${status}`);
      li.dataset.id = id;
      li.querySelector('.m-name').textContent = m.name;
      li.querySelector('.m-status').textContent = STATUS_TEXT[status];
      li.querySelector('.m-uptime').textContent = pct(m.uptime_24h);
      const last = li.querySelector('.m-last');
      last.textContent = ago(m.last_checked_at);
      last.title = localTime(m.last_checked_at);

      const btn = li.querySelector('.monitor-row');
      const detail = li.querySelector('.detail');
      btn.setAttribute('aria-controls', `detail-${id}`);
      detail.id = `detail-${id}`;
      btn.addEventListener('click', () => toggleMonitor(id, m.name));
      if (id === openMonitorId) {
        btn.setAttribute('aria-expanded', 'true');
        detail.hidden = false;
        detail.appendChild(el('p', 'nodata', `Загружаем uptime «${m.name}»…`));
      }
      frag.appendChild(li);
    }
    els.monitors.replaceChildren(frag);
    // the chart is sized from the real container width, so draw it only after it is in the DOM
    if (openMonitorId !== null && lastUptime && lastUptime.id === openMonitorId) {
      renderUptime(detailOf(openMonitorId), lastUptime.items);
    }
  }

  function renderIncidents(incidents) {
    els.incidentsEmpty.hidden = incidents.length > 0;
    const frag = document.createDocumentFragment();
    for (const i of incidents) {
      const li = el('li', 'incident');
      li.appendChild(el('span', 'i-name', i.monitor_name || `монитор #${i.monitor_id}`));
      const since = el('span', 'i-since', `идёт ${duration(i.started_at)}`);
      since.title = `с ${localTime(i.started_at)}`;
      li.appendChild(since);
      li.appendChild(el('span', 'i-cause', i.cause || 'причина не указана'));
      frag.appendChild(li);
    }
    els.incidents.replaceChildren(frag);
  }

  async function refresh() {
    clearTimeout(timer);
    try {
      const data = await getJSON(STATUS_URL);
      renderOverall(data);
      renderMonitors(data.monitors || []);
      renderIncidents(data.open_incidents || []);
      els.updated.textContent = `данные на ${new Date(data.generated_at).toLocaleTimeString('ru-RU')}`;
      els.updated.title = `сгенерировано ${localTime(data.generated_at)}`;
      showError('');
      if (openMonitorId !== null) loadUptime(openMonitorId);
    } catch (e) {
      // keep the last good data on screen, just say it is stale
      showError(`Не удалось обновить статус (${e.message}). Повторим через 15 с.`);
      if (els.overall.classList.contains('overall-loading')) {
        els.overallTitle.textContent = 'Статус недоступен';
      }
    } finally {
      timer = setTimeout(refresh, REFRESH_MS);
    }
  }

  // ---------- uptime chart ----------

  function detailOf(id) {
    const li = els.monitors.querySelector(`li[data-id="${CSS.escape(id)}"]`);
    return li ? li.querySelector('.detail') : null;
  }

  function toggleMonitor(id, name) {
    const wasOpen = openMonitorId === id;
    for (const li of els.monitors.children) {
      li.querySelector('.monitor-row').setAttribute('aria-expanded', 'false');
      li.querySelector('.detail').hidden = true;
    }
    openMonitorId = wasOpen ? null : id;
    if (wasOpen) return;

    const detail = detailOf(id);
    detail.hidden = false;
    detail.previousElementSibling.setAttribute('aria-expanded', 'true');
    if (lastUptime && lastUptime.id === id) {
      renderUptime(detail, lastUptime.items);
    } else {
      detail.replaceChildren(el('p', 'nodata', `Загружаем uptime «${name}»…`));
    }
    loadUptime(id);
  }

  async function loadUptime(id) {
    let items;
    try {
      items = (await getJSON(uptimeUrl(id))).items || [];
    } catch (e) {
      const detail = detailOf(id);
      if (detail && openMonitorId === id) {
        detail.replaceChildren(el('p', 'nodata', `Не удалось загрузить uptime: ${e.message}`));
      }
      return;
    }
    lastUptime = { id, items };
    const detail = detailOf(id);
    if (!detail || openMonitorId !== id) return;
    renderUptime(detail, items);
  }

  function barClass(uptime) {
    if (uptime === null) return 'b-none';
    if (uptime >= 0.99) return 'b-up';
    if (uptime >= 0.9) return 'b-degraded';
    return 'b-down';
  }

  function renderUptime(detail, items) {
    if (items.length === 0) {
      const p = el('p', 'nodata');
      p.append(
        'Нет данных за 7 дней. Почасовой uptime считает aggregator из Kafka — включите Kafka-конвейер (',
        el('code', null, 'KAFKA_BROKERS'),
        ' у checker и aggregator).',
      );
      detail.replaceChildren(p);
      return;
    }

    let checks = 0;
    let ok = 0;
    let latencySum = 0;
    const byHour = new Map();
    for (const it of items) {
      byHour.set(new Date(it.hour).getTime(), it);
      checks += it.checks;
      ok += it.ok;
      latencySum += (it.latency_avg_ms || 0) * it.checks;
    }

    const head = el('div', 'detail-head');
    head.append(
      el('strong', null, `Uptime за 7 дней: ${pct(checks ? ok / checks : null, 3)}`),
      el('span', null, `проверок: ${checks.toLocaleString('ru-RU')}`),
      el('span', null, `средняя задержка: ${checks ? Math.round(latencySum / checks) : '—'} мс`),
    );

    const legend = el('div', 'legend');
    for (const [cls, text] of [['l-up', '≥ 99%'], ['l-degraded', '90–99%'], ['l-down', '< 90%'], ['l-none', 'нет данных']]) {
      const s = el('span');
      s.append(el('i', cls), text);
      legend.appendChild(s);
    }

    detail.replaceChildren(head, buildChart(byHour, detail.clientWidth - 2), legend);
  }

  // Inline SVG in real pixels (not a scaled viewBox), so labels stay readable on a 360 px phone.
  function buildChart(byHour, width) {
    const NS = 'http://www.w3.org/2000/svg';
    const W = Math.max(280, Math.floor(width) || 600);
    const H = 150;
    const left = 36;
    const right = 4;
    const top = 8;
    const plotH = 110;
    const slot = (W - left - right) / CHART_HOURS;
    const gap = slot > 3 ? 1 : 0;

    const svg = document.createElementNS(NS, 'svg');
    svg.setAttribute('class', 'chart');
    svg.setAttribute('width', String(W));
    svg.setAttribute('height', String(H));
    svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
    svg.setAttribute('role', 'img');
    svg.setAttribute('aria-label', 'Uptime по часам за 7 дней');

    const add = (tag, attrs, text) => {
      const n = document.createElementNS(NS, tag);
      for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, String(v));
      if (text !== undefined) n.textContent = text;
      svg.appendChild(n);
      return n;
    };

    for (const [v, label] of [[1, '100%'], [0.5, '50%'], [0, '0%']]) {
      const y = top + plotH * (1 - v);
      add('line', { class: 'grid', x1: left, x2: W - right, y1: y, y2: y });
      add('text', { class: 'axis', x: left - 6, y: y + 3, 'text-anchor': 'end' }, label);
    }

    const end = Math.floor(Date.now() / HOUR_MS) * HOUR_MS; // current hour (UTC == local for whole-hour zones)
    for (let i = 0; i < CHART_HOURS; i++) {
      const t = end - (CHART_HOURS - 1 - i) * HOUR_MS;
      const d = new Date(t);
      const x = left + i * slot;
      if (d.getHours() === 0) {
        add('line', { class: 'grid', x1: x, x2: x, y1: top, y2: top + plotH + 4 });
        const label = d.toLocaleDateString('ru-RU', { day: '2-digit', month: '2-digit' });
        add('text', { class: 'axis', x: x + 2, y: top + plotH + 16 }, label);
      }
      const it = byHour.get(t);
      const uptime = it && it.uptime !== null ? it.uptime : null;
      const h = uptime === null ? 3 : Math.max(2, plotH * uptime);
      if (uptime !== null && uptime < 0.9) {
        // a bad hour is a short bar, easy to miss: shade the whole column
        add('rect', { class: 'b-down-bg', x: x.toFixed(2), y: top, width: Math.max(1, slot - gap).toFixed(2), height: plotH });
      }
      const rect = add('rect', {
        class: barClass(uptime),
        x: x.toFixed(2),
        y: (top + plotH - h).toFixed(2),
        width: Math.max(1, slot - gap).toFixed(2),
        height: h.toFixed(2),
      });
      const when = d.toLocaleString('ru-RU', { day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit' });
      const title = document.createElementNS(NS, 'title');
      title.textContent = it
        ? `${when}: uptime ${pct(it.uptime)}, проверок ${it.checks}, ок ${it.ok}, задержка ср. ${it.latency_avg_ms ?? '—'} мс / макс. ${it.latency_max_ms} мс`
        : `${when}: нет данных`;
      rect.appendChild(title);
    }
    return svg;
  }

  // ---------- wiring ----------

  let resizeTimer = null;
  window.addEventListener('resize', () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      if (openMonitorId === null || !lastUptime || lastUptime.id !== openMonitorId) return;
      const detail = detailOf(openMonitorId);
      if (detail) renderUptime(detail, lastUptime.items);
    }, 150);
  });

  // a background tab does not need to poll; refresh immediately when it becomes visible again
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) clearTimeout(timer);
    else refresh();
  });

  refresh();
})();
