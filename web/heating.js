// heating.js — heat-pump telemetry card on the main dashboard.
//
// Read-only view over the MyUplink driver's hp_* metrics (compressor power +
// hot-water/indoor/outdoor temperatures). The section stays hidden until a
// driver actually reports hp_power_w, so a site without a heat pump never
// sees an empty card. Discovery runs once on load (one /api/drivers/{name}
// fetch per driver); steady-state polling then only touches the heat-pump
// drivers, so remote routes don't pay for every driver every 30 s.
//
// See docs/myuplink-oauth.md. No control here — telemetry only.

(function () {
  'use strict';

  var REFRESH_MS = 30000;
  var timer = null;
  var heatPumpDrivers = null; // cached after discovery: array of driver names
  var lastDiscoverMs = 0;             // Date.now() of the last discovery scan
  var DISCOVER_EVERY_MS = 300000;     // re-scan for newly-added heat pumps (5 min)

  // Route reads over the owner/P2P transport when present (remote home
  // route), else plain fetch (LAN / tests). Mirrors twins.js.
  function ownerFetch(path, opts) {
    if (typeof window.ownerFetch === 'function') return window.ownerFetch(path, opts);
    return fetch(path, opts);
  }

  // ── Card formatters ──────────────────────────────────────────────
  function fmtPower(v) {
    if (v == null) return '—';
    if (Math.abs(v) >= 1000) return (v / 1000).toFixed(2) + ' kW';
    return Math.round(v) + ' W';
  }
  function fmtTemp(v)   { return v == null ? '—' : v.toFixed(1) + ' °C'; }
  function fmtKW(v)     { return v == null ? '—' : v.toFixed(2) + ' kW'; }
  function fmtHz(v)     { return v == null ? '—' : Math.round(v) + ' Hz'; }
  function fmtAmp(v)    { return v == null ? '—' : v.toFixed(1) + ' A'; }
  function fmtPct(v)    { return v == null ? '—' : Math.round(v) + ' %'; }
  function fmtFlow(v)   { return v == null ? '—' : Math.round(v) + ' m³/h'; }
  function fmtDM(v)     { return v == null ? '—' : Math.round(v) + ' DM'; }
  function fmtKwh(v)    { return v == null ? '—' : Math.round(v).toLocaleString('sv-SE') + ' kWh'; }
  function fmtRaw(v)    { return v == null ? '—' : String(Math.round(v * 100) / 100); }
  function fmtOffset(v) { return v == null ? '—' : (v > 0 ? '+' : '') + Math.round(v); }
  var PRIO = { 0: 'av', 10: 'av', 20: 'varmvatten', 30: 'värme', 40: 'pool', 60: 'kyla' };
  function fmtPrio(v) { if (v == null) return '—'; var n = Math.round(v); return n + (PRIO[n] ? ' · ' + PRIO[n] : ''); }
  function fmtVent(v) { if (v == null) return '—'; var n = Math.round(v); return n === 0 ? 'Normal' : String(n); }

  // ── Card layout: grouped tiles. Each item = { key (hp_* metric), label,
  // optional sensor designation (BT21 …), formatter, info (hover tooltip on
  // the ⓘ icon) }. Render order = array order. The detail pop-up still lists
  // ALL ~960 signals; this is the curated at-a-glance set.
  var GROUPS = [
    { title: 'Effekt & el', items: [
      { key: 'hp_energy_log_current_power_consumption', label: 'Total effekt nu', fmt: fmtKW, info: 'Hela värmepumpens momentana elförbrukning just nu — kompressor + fläkt + cirkulationspumpar + elektronik.' },
      { key: 'hp_power_w', label: 'Kompressor', fmt: fmtPower, info: 'Effekt till enbart kompressorn. 0 W när kompressorn står still — pumpen drar ändå el (se Total effekt nu).' },
      { key: 'hp_power_internal_additional_heat', label: 'Intern tillsats', fmt: fmtKW, info: 'Effekt till den interna elpatronen (tillskottsvärme). 0 när bara kompressorn jobbar.' },
      { key: 'hp_compressor_frequency_current', label: 'Kompr.frekvens', fmt: fmtHz, info: 'Kompressorns frekvens/varvtal just nu. 0 Hz = kompressorn står still.' },
      { key: 'hp_current_be1', label: 'Ström', sensor: 'BE1', fmt: fmtAmp, info: 'Uppmätt ström på fas 1 (strömtransformator BE1). Används för effektvakt/säkringsskydd.' },
      { key: 'hp_current_be2', label: 'Ström', sensor: 'BE2', fmt: fmtAmp, info: 'Uppmätt ström på fas 2 (BE2).' },
      { key: 'hp_current_be3', label: 'Ström', sensor: 'BE3', fmt: fmtAmp, info: 'Uppmätt ström på fas 3 (BE3).' },
    ] },
    { title: 'Temperaturer', items: [
      { key: 'hp_hw_top_temp_c', label: 'Varmvatten topp', sensor: 'BT7', fmt: fmtTemp, info: 'Temperatur högst upp i varmvattenberedaren — det du får först ur kranen.' },
      { key: 'hp_hot_water_charging_bt6', label: 'VV laddning', sensor: 'BT6', fmt: fmtTemp, info: 'Styrande givare för varmvattenladdning — avgör när tanken är fulladdad.' },
      { key: 'hp_hot_water_start_bt5', label: 'VV start', sensor: 'BT5', fmt: fmtTemp, info: 'Startgivare för varmvatten — startar ny laddning när den faller under startvärdet.' },
      { key: 'hp_supply_line_bt2', label: 'Framledning', sensor: 'BT2', fmt: fmtTemp, info: 'Temperatur på vattnet UT till värmesystemet (framledning).' },
      { key: 'hp_return_line_bt3', label: 'Returledning', sensor: 'BT3', fmt: fmtTemp, info: 'Temperatur på vattnet TILLBAKA från värmesystemet (retur).' },
      { key: 'hp_calculated_supply_climate_system_1', label: 'Ber. framledning', fmt: fmtTemp, info: 'Beräknad (önskad) framledning som styrsystemet räknat fram ur värmekurvan.' },
      { key: 'hp_fr_nluft_bt20', label: 'Frånluft', sensor: 'BT20', fmt: fmtTemp, info: 'Ventilationsluften som sugs ut från rummen — värmepumpens värmekälla (in i förångaren).' },
      { key: 'hp_avluft_bt21', label: 'Avluft', sensor: 'BT21', fmt: fmtTemp, info: 'Luften efter värmeåtervinning, på väg ut ur huset. Frånluft − avluft = återvunnen värme.' },
      { key: 'hp_outdoor_temp_c', label: 'Utomhus', sensor: 'BT1', fmt: fmtTemp, info: 'Utomhustemperatur (BT1) — styr värmekurvan.' },
    ] },
    { title: 'Ventilation', items: [
      { key: 'hp_ventilation_mode', label: 'Ventilationsläge', fmt: fmtVent, info: 'Aktivt ventilationsläge. 0 = normal.' },
      { key: 'hp_exhaust_air_fan_speed_gq2', label: 'Fläkthastighet', sensor: 'GQ2', fmt: fmtPct, info: 'Frånluftsfläktens varvtal just nu. Normal = 54 %; lägre (t.ex. 30 %) = reducerad ventilation (hastighet 2).' },
      { key: 'hp_real_air_flow', label: 'Luftflöde', fmt: fmtFlow, info: 'Uppmätt luftflöde genom aggregatet.' },
    ] },
    { title: 'Drift', items: [
      { key: 'hp_priority', label: 'Prio', fmt: fmtPrio, info: 'Vad kompressorn prioriterar just nu: 10 = av, 20 = varmvatten, 30 = värme, 40 = pool, 60 = kyla.' },
      { key: 'hp_degree_minutes', label: 'Gradminuter', fmt: fmtDM, info: 'Värmeunderskott integrerat över tid. Når det startgränsen startar kompressorn. 0 = inget underskott (värme avstängd på sommaren).' },
      { key: 'hp_heating_medium_pump_speed_gp1', label: 'Värmebärarpump', sensor: 'GP1', fmt: fmtPct, info: 'Cirkulationspumpens (GP1) varvtal i värmesystemet.' },
      { key: 'hp_heating_curve_climate_system_1', label: 'Värmekurva', fmt: fmtRaw, info: 'Inställd värmekurva (lutning) för klimatsystem 1. Högre = varmare framledning när det är kallt ute.' },
      { key: 'hp_heating_offset_climate_system_1', label: 'Kurvförskjutning', fmt: fmtOffset, info: 'Parallellförskjutning av värmekurvan — varmare (+) eller kallare (−) överlag.' },
    ] },
    { title: 'Energi (totalt)', items: [
      { key: 'hp_energy_consumed_kwh', label: 'Total förbrukning', fmt: fmtKwh, info: 'Total tillförd el till värmepumpen sedan start (livstidsräknare).' },
      { key: 'hp_energy_produced_kwh', label: 'Total produktion', fmt: fmtKwh, info: 'Total avgiven värmeenergi sedan start. Produktion ÷ förbrukning ≈ värmefaktor (SCOP).' },
      { key: 'hp_heating_compressor_only', label: 'Värme (kompr.)', fmt: fmtKwh, info: 'Avgiven värme till uppvärmning, endast från kompressorn (exkl. elpatron).' },
      { key: 'hp_hot_water_compressor_only', label: 'Varmvatten (kompr.)', fmt: fmtKwh, info: 'Avgiven värme till varmvatten, endast från kompressorn (exkl. elpatron).' },
    ] },
  ];

  function injectStyles() {
    if (document.getElementById('ftw-heating-styles')) return;
    var css = [
      '#heating-grid{display:flex;flex-direction:column;gap:18px}',
      '.ftw-hp{display:flex;flex-direction:column;gap:16px}',
      '.ftw-hp-clickable{cursor:pointer;border-radius:8px;margin:-6px;padding:6px;transition:background 0.12s}',
      '.ftw-hp-clickable:hover,.ftw-hp-clickable:focus{background:var(--bg-hover,rgba(127,127,127,0.06));outline:none}',
      '.ftw-hp-head{display:flex;align-items:baseline;justify-content:space-between;gap:10px}',
      '.ftw-hp-more{font-family:var(--mono);font-size:0.66rem;letter-spacing:0.1em;text-transform:uppercase;color:var(--accent-e)}',
      '.ftw-hp-name{font-family:var(--mono);font-size:0.72rem;letter-spacing:0.18em;text-transform:uppercase;color:var(--fg-muted)}',
      '.ftw-hp-group{display:flex;flex-direction:column;gap:7px}',
      '.ftw-hp-group-title{font-family:var(--mono);font-size:0.6rem;letter-spacing:0.16em;text-transform:uppercase;color:var(--accent-e)}',
      '.ftw-hp-tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(120px,1fr));gap:10px 18px}',
      '.ftw-hp-tile{display:flex;flex-direction:column;gap:3px}',
      '.ftw-hp-tile-label{font-family:var(--mono);font-size:0.64rem;letter-spacing:0.08em;text-transform:uppercase;color:var(--fg-muted);display:flex;align-items:baseline;gap:3px;flex-wrap:wrap}',
      '.ftw-hp-sensor{color:var(--fg-muted);opacity:0.6}',
      '.ftw-hp-i{cursor:help;color:var(--fg-muted);opacity:0.5;align-self:center}',
      '.ftw-hp-tile-val{font-family:var(--mono);font-size:1.02rem;font-variant-numeric:tabular-nums;color:var(--fg)}',
      '.ftw-hp-spark{display:flex;flex-direction:column;gap:4px}',
      '.ftw-hp-spark-label{font-family:var(--mono);font-size:0.66rem;letter-spacing:0.12em;text-transform:uppercase;color:var(--fg-muted)}',
      '.ftw-hp-spark svg{width:100%;height:48px;display:block}',
      '.ftw-hp-empty{font-family:var(--mono);font-size:0.8rem;color:var(--fg-muted)}',
    ].join('');
    var el = document.createElement('style');
    el.id = 'ftw-heating-styles';
    el.textContent = css;
    document.head.appendChild(el);
  }

  // Build a sparkline <svg> from [{ts, v}] points. Pure SVG so it themes via
  // CSS vars (stroke = accent). Returns '' when there's nothing to draw.
  function sparkline(points) {
    if (!points || points.length < 2) return '';
    var w = 240, h = 48, pad = 3;
    var vals = points.map(function (p) { return p.v; });
    var min = Math.min.apply(null, vals);
    var max = Math.max.apply(null, vals);
    var span = max - min || 1;
    var n = points.length;
    var coords = points.map(function (p, i) {
      var x = pad + (i / (n - 1)) * (w - 2 * pad);
      var y = h - pad - ((p.v - min) / span) * (h - 2 * pad);
      return x.toFixed(1) + ',' + y.toFixed(1);
    });
    return '<svg viewBox="0 0 ' + w + ' ' + h + '" preserveAspectRatio="none" aria-hidden="true">' +
      '<polyline fill="none" stroke="var(--accent-e)" stroke-width="1.5" ' +
      'stroke-linejoin="round" stroke-linecap="round" points="' + coords.join(' ') + '"></polyline>' +
      '</svg>';
  }

  function metricMap(metrics) {
    var m = {};
    (metrics || []).forEach(function (s) { if (s && s.name) m[s.name] = s.value; });
    return m;
  }

  function isHeatPump(detail) {
    var m = metricMap(detail && detail.metrics);
    return Object.prototype.hasOwnProperty.call(m, 'hp_power_w');
  }

  function fetchJSON(path) {
    return ownerFetch(path).then(function (r) { return r.json(); }).catch(function () { return null; });
  }

  // One-time discovery: list drivers, fetch each detail, keep the ones that
  // report hp_power_w.
  function discover() {
    return fetchJSON('/api/drivers').then(function (health) {
      if (!health || typeof health !== 'object') return [];
      var names = Object.keys(health);
      return Promise.all(names.map(function (n) {
        return fetchJSON('/api/drivers/' + encodeURIComponent(n)).then(function (d) {
          return d && isHeatPump(d) ? n : null;
        });
      })).then(function (found) {
        return found.filter(Boolean);
      });
    });
  }

  function tileHtml(def, m) {
    var has = Object.prototype.hasOwnProperty.call(m, def.key);
    var sensor = def.sensor ? ' <span class="ftw-hp-sensor">(' + escapeHtml(def.sensor) + ')</span>' : '';
    var info = def.info ? ' <span class="ftw-hp-i" role="img" aria-label="info" title="' + escapeHtml(def.info) + '">ⓘ</span>' : '';
    return '<div class="ftw-hp-tile">' +
      '<span class="ftw-hp-tile-label">' + escapeHtml(def.label) + sensor + info + '</span>' +
      '<span class="ftw-hp-tile-val">' + (has ? def.fmt(m[def.key]) : '—') + '</span>' +
      '</div>';
  }

  function renderPump(name, detail, sparkPoints) {
    var m = metricMap(detail && detail.metrics);
    var groups = GROUPS.map(function (g) {
      var tiles = g.items.map(function (def) { return tileHtml(def, m); }).join('');
      return '<div class="ftw-hp-group"><div class="ftw-hp-group-title">' + escapeHtml(g.title) + '</div>' +
        '<div class="ftw-hp-tiles">' + tiles + '</div></div>';
    }).join('');
    var spark = sparkline(sparkPoints);
    var sparkBlock = spark
      ? '<div class="ftw-hp-spark"><span class="ftw-hp-spark-label">Kompressoreffekt · 24h</span>' + spark + '</div>'
      : '';
    // The whole card is a button into the detail view (all signals + register).
    return '<div class="ftw-hp ftw-hp-clickable" data-hp-driver="' + escapeHtml(name) + '" role="button" tabindex="0" title="Visa alla signaler">' +
      '<div class="ftw-hp-head"><span class="ftw-hp-name">' + escapeHtml(name) + '</span>' +
      '<span class="ftw-hp-more">Alla signaler →</span></div>' +
      groups +
      sparkBlock +
      '</div>';
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function refresh() {
    var section = document.getElementById('heating-section');
    var grid = document.getElementById('heating-grid');
    if (!section || !grid) return;

    // Re-run discovery on first call, then periodically — so a heat-pump
    // driver added while the dashboard is open shows up without a manual
    // reload. (The old code cached forever; an empty result is also truthy,
    // so a site that discovered before its pump reported hp_power_w stayed
    // blank.) Steady-state stays cheap: between scans we only touch the
    // already-known heat-pump drivers.
    var nowMs = Date.now();
    var rediscover = heatPumpDrivers === null || (nowMs - lastDiscoverMs) >= DISCOVER_EVERY_MS;
    var ready = rediscover
      ? discover().then(function (names) { heatPumpDrivers = names; lastDiscoverMs = nowMs; return names; })
      : Promise.resolve(heatPumpDrivers);

    ready.then(function (names) {
      if (!names || names.length === 0) {
        section.hidden = true;
        return;
      }
      // Fetch detail + 24h power series for each heat pump in parallel.
      return Promise.all(names.map(function (n) {
        return Promise.all([
          fetchJSON('/api/drivers/' + encodeURIComponent(n)),
          fetchJSON('/api/series?driver=' + encodeURIComponent(n) + '&metric=hp_power_w&range=24h&points=200'),
        ]).then(function (parts) {
          return { name: n, detail: parts[0], series: (parts[1] && parts[1].points) || [] };
        });
      })).then(function (pumps) {
        var live = pumps.filter(function (p) { return p.detail && isHeatPump(p.detail); });
        if (live.length === 0) { section.hidden = true; return; }
        injectStyles();
        section.hidden = false;
        grid.innerHTML = live.map(function (p) {
          return renderPump(p.name, p.detail, p.series);
        }).join('');
      });
    });
  }

  // ---- Detail drill-in: all points grouped by unit ----

  // Ordered unit groups. First matching predicate wins; anything unmatched
  // falls into "State / other".
  var UNIT_GROUPS = [
    { title: 'Temperatures', match: function (u) { return u === '°C' || u === '°F' || u === 'K'; } },
    { title: 'Power & energy', match: function (u) { return u === 'W' || u === 'kW' || u === 'Wh' || u === 'kWh'; } },
    { title: 'Frequency', match: function (u) { return u === 'Hz'; } },
    { title: 'Percent', match: function (u) { return u === '%'; } },
    { title: 'Flow & pressure', match: function (u) { return u === 'l/m' || u === 'l/min' || u === 'bar' || u === 'kPa'; } },
    { title: 'Electrical', match: function (u) { return u === 'A' || u === 'V'; } },
    { title: 'Counters & degree-minutes', match: function (u) { return u === 'GM' || u === 'DM' || u === 'h' || u === 'min' || u === 's' || /count/i.test(u); } },
  ];

  function groupForUnit(unit) {
    for (var i = 0; i < UNIT_GROUPS.length; i++) {
      if (UNIT_GROUPS[i].match(unit || '')) return UNIT_GROUPS[i].title;
    }
    return 'State / other';
  }

  // hp_supply_line_bt2 → "Supply line bt2"
  function prettyLabel(name) {
    var s = String(name).replace(/^hp_/, '').replace(/_/g, ' ').trim();
    return s.charAt(0).toUpperCase() + s.slice(1);
  }

  function fmtValue(v, unit) {
    if (v == null) return '—';
    var n = Math.abs(v) >= 100 ? Math.round(v) : Math.round(v * 100) / 100;
    return n + (unit ? ' ' + unit : '');
  }

  function injectDetailStyles() {
    if (document.getElementById('ftw-heating-detail-styles')) return;
    var css = [
      '.ftw-hpd-backdrop{position:fixed;inset:0;background:rgba(0,0,0,0.55);display:flex;align-items:flex-start;justify-content:center;z-index:1000;overflow:auto;padding:5vh 16px}',
      '.ftw-hpd{background:var(--ink-raised);border:1px solid var(--line);border-radius:10px;max-width:760px;width:100%;padding:20px 22px}',
      '.ftw-hpd-top{display:flex;align-items:baseline;justify-content:space-between;gap:12px;margin-bottom:14px}',
      '.ftw-hpd-title{font-family:var(--mono);font-size:0.74rem;letter-spacing:0.18em;text-transform:uppercase;color:var(--fg-muted)}',
      '.ftw-hpd-close{background:none;border:1px solid var(--line);color:var(--fg);border-radius:6px;cursor:pointer;font-size:1rem;line-height:1;padding:4px 9px}',
      '.ftw-hpd-group{margin:16px 0 4px;font-family:var(--mono);font-size:0.68rem;letter-spacing:0.14em;text-transform:uppercase;color:var(--accent-e)}',
      '.ftw-hpd-row{display:grid;grid-template-columns:1fr auto auto 120px;gap:10px 14px;align-items:center;padding:5px 0;border-bottom:1px solid var(--line-soft,var(--line))}',
      '.ftw-hpd-label{color:var(--fg-muted);font-size:0.85rem}',
      '.ftw-hpd-reg{font-family:var(--mono);font-variant-numeric:tabular-nums;font-size:0.76rem;color:var(--fg-muted);text-align:right;opacity:0.8}',
      '.ftw-hpd-val{font-family:var(--mono);font-variant-numeric:tabular-nums;color:var(--fg);text-align:right}',
      '.ftw-hpd-spark svg{width:120px;height:24px;display:block}',
      '.ftw-hpd-empty{color:var(--fg-muted);font-family:var(--mono);font-size:0.82rem}',
    ].join('');
    var el = document.createElement('style');
    el.id = 'ftw-heating-detail-styles';
    el.textContent = css;
    document.head.appendChild(el);
  }

  function closeDetail() {
    var b = document.getElementById('ftw-hpd-backdrop');
    if (b) b.remove();
    document.removeEventListener('keydown', onDetailKey);
  }
  function onDetailKey(e) { if (e.key === 'Escape') closeDetail(); }

  function openDetail(name) {
    injectDetailStyles();
    closeDetail();
    var backdrop = document.createElement('div');
    backdrop.className = 'ftw-hpd-backdrop';
    backdrop.id = 'ftw-hpd-backdrop';
    backdrop.innerHTML = '<div class="ftw-hpd">' +
      '<div class="ftw-hpd-top"><span class="ftw-hpd-title">Heat pump · ' + escapeHtml(name) + '</span>' +
      '<button class="ftw-hpd-close" type="button" aria-label="Close">✕</button></div>' +
      '<div id="ftw-hpd-body"><div class="ftw-hpd-empty">Loading signals…</div></div></div>';
    backdrop.addEventListener('click', function (e) { if (e.target === backdrop) closeDetail(); });
    backdrop.querySelector('.ftw-hpd-close').addEventListener('click', closeDetail);
    document.body.appendChild(backdrop);
    document.addEventListener('keydown', onDetailKey);

    fetchJSON('/api/drivers/' + encodeURIComponent(name)).then(function (d) {
      var body = document.getElementById('ftw-hpd-body');
      if (!body) return;
      var metrics = (d && d.metrics) || [];
      if (!metrics.length) { body.innerHTML = '<div class="ftw-hpd-empty">No signals reported yet.</div>'; return; }
      // Bucket metrics into ordered groups.
      var buckets = {};
      metrics.forEach(function (m) {
        var g = groupForUnit(m.unit);
        (buckets[g] = buckets[g] || []).push(m);
      });
      var order = UNIT_GROUPS.map(function (g) { return g.title; }).concat(['State / other']);
      var html = '';
      order.forEach(function (g) {
        var rows = buckets[g];
        if (!rows || !rows.length) return;
        rows.sort(function (a, b) { return a.name < b.name ? -1 : 1; });
        html += '<div class="ftw-hpd-group">' + escapeHtml(g) + '</div>';
        rows.forEach(function (m) {
          html += '<div class="ftw-hpd-row">' +
            '<span class="ftw-hpd-label">' + escapeHtml(prettyLabel(m.name)) + '</span>' +
            '<span class="ftw-hpd-reg" title="Modbus register">' + (m.register ? escapeHtml(String(m.register)) : '—') + '</span>' +
            '<span class="ftw-hpd-val">' + escapeHtml(fmtValue(m.value, m.unit)) + '</span>' +
            '<span class="ftw-hpd-spark" data-spark-metric="' + escapeHtml(m.name) + '"></span>' +
            '</div>';
        });
      });
      body.innerHTML = html;
      // Lazily fill sparklines (one /api/series per metric) — values already
      // shown, so a slow series fetch never blocks the table.
      metrics.forEach(function (m) {
        fetchJSON('/api/series?driver=' + encodeURIComponent(name) + '&metric=' + encodeURIComponent(m.name) + '&range=24h&points=120')
          .then(function (s) {
            var slot = body.querySelector('.ftw-hpd-spark[data-spark-metric="' + (window.CSS && CSS.escape ? CSS.escape(m.name) : m.name) + '"]');
            if (slot) slot.innerHTML = sparkline((s && s.points) || []);
          });
      });
    });
  }

  function onGridClick(e) {
    var card = e.target.closest && e.target.closest('.ftw-hp-clickable');
    if (card && card.dataset.hpDriver) openDetail(card.dataset.hpDriver);
  }

  function start() {
    if (timer) return;
    var grid = document.getElementById('heating-grid');
    if (grid) {
      grid.addEventListener('click', onGridClick);
      grid.addEventListener('keydown', function (e) {
        if ((e.key === 'Enter' || e.key === ' ') && e.target.classList && e.target.classList.contains('ftw-hp-clickable')) {
          e.preventDefault(); onGridClick(e);
        }
      });
    }
    refresh();
    timer = setInterval(refresh, REFRESH_MS);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', start);
  } else {
    start();
  }
})();
