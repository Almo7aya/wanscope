'use strict';
'require baseclass';
'require rpc';
'require ui';

// Shared code for the wanscope views: calls to the daemon, formatting, colours and the SVG charts.
// Colours: the dataviz reference palette in fixed slot order, given to uplinks by their order and to devices in the
// order they first appear on a page (a colour follows its uplink or device, never its rank). Status colours are
// reserved for good / bad states and always come with words.

var callStatus = rpc.declare({ object: 'wanscope', method: 'status' });
var callLive = rpc.declare({ object: 'wanscope', method: 'live', params: [ 'since', 'window', 'devices' ] });
var callDevices = rpc.declare({ object: 'wanscope', method: 'devices' });
var callDevice = rpc.declare({ object: 'wanscope', method: 'device', params: [ 'key', 'from', 'to' ] });
var callWans = rpc.declare({ object: 'wanscope', method: 'wans' });
var callFlows = rpc.declare({ object: 'wanscope', method: 'flows', params: [ 'device', 'wan', 'limit' ] });
var callHistory = rpc.declare({ object: 'wanscope', method: 'history', params: [ 'keys', 'from', 'to', 'step' ] });
var callTop = rpc.declare({ object: 'wanscope', method: 'top', params: [ 'groups', 'from', 'to', 'limit' ] });
var callUsage = rpc.declare({ object: 'wanscope', method: 'usage', params: [ 'keys', 'from', 'to' ] });
var callAlerts = rpc.declare({ object: 'wanscope', method: 'alerts', params: [ 'limit' ] });
var callReport = rpc.declare({ object: 'wanscope', method: 'report', params: [ 'from', 'to' ] });
var callNotifyTest = rpc.declare({ object: 'wanscope', method: 'notify_test' });
var callSetDevice = rpc.declare({ object: 'wanscope', method: 'set_device',
	params: [ 'key', 'name', 'icon', 'block', 'pause', 'down', 'up', 'schedules', 'quotas', 'approved' ] });
var callSetWan = rpc.declare({ object: 'wanscope', method: 'set_wan', params: [ 'name', 'label', 'kind', 'hidden' ] });
var callPlans = rpc.declare({ object: 'wanscope', method: 'plans' });
var callSetPlan = rpc.declare({ object: 'wanscope', method: 'set_plan', params: [ 'id', 'name', 'type', 'size_gb', 'ifaces', 'cycle_day', 'valid_days', 'used_gb' ] });
var callTopup = rpc.declare({ object: 'wanscope', method: 'topup', params: [ 'id', 'size_gb', 'mode' ] });
var callCalibrate = rpc.declare({ object: 'wanscope', method: 'calibrate', params: [ 'id', 'left_gb' ] });
var callDeletePlan = rpc.declare({ object: 'wanscope', method: 'delete_plan', params: [ 'id' ] });
var callSetGuest = rpc.declare({ object: 'wanscope', method: 'set_guest', params: [ 'policy', 'down', 'up' ] });
var callForget = rpc.declare({ object: 'wanscope', method: 'forget', params: [ 'key' ] });
var callBlocks = rpc.declare({ object: 'wanscope', method: 'blocks' });
var callBlockService = rpc.declare({ object: 'wanscope', method: 'block_service', params: [ 'service', 'device', 'block' ] });
var callSetRule = rpc.declare({ object: 'wanscope', method: 'set_rule', params: [ 'name', 'category', 'domain', 'cidr', 'port' ] });
var callUpdate = rpc.declare({ object: 'wanscope', method: 'update', params: [ 'check' ] });
var callUpdateStart = rpc.declare({ object: 'wanscope', method: 'update_start' });

var SVGNS = 'http://www.w3.org/2000/svg';

var LIGHT = 'color-scheme:light;--ws-c1:#2a78d6;--ws-c2:#eb6834;--ws-c3:#1baf7a;--ws-c4:#eda100;--ws-c5:#e87ba4;--ws-c6:#4a3aa7;--ws-c7:#e34948;--ws-c8:#008300;' +
	'--ws-other:#8c8c8c;--ws-good:#0ca30c;--ws-bad:#d03b3b;--ws-warn:#c98500;' +
	'--ws-grid:rgba(0,0,0,.08);--ws-axis:rgba(0,0,0,.28);--ws-line:rgba(128,128,128,.28);--ws-fill:rgba(128,128,128,.055);--ws-track:rgba(128,128,128,.16)';
var DARK = 'color-scheme:dark;--ws-c1:#3987e5;--ws-c2:#d95926;--ws-c3:#199e70;--ws-c4:#c98500;--ws-c5:#d55181;--ws-c6:#9085e9;--ws-c7:#e66767;--ws-c8:#0ca30c;' +
	'--ws-other:#8c8c8c;--ws-good:#2fbf4a;--ws-bad:#e66767;--ws-warn:#e0a020;' +
	'--ws-grid:rgba(255,255,255,.08);--ws-axis:rgba(255,255,255,.25);--ws-line:rgba(160,160,160,.25);--ws-fill:rgba(160,160,160,.06);--ws-track:rgba(160,160,160,.18)';

var CSS = [
	'.ws{' + LIGHT + ';font:13px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}',
	':root[data-darkmode="true"] .ws{' + DARK + '}',
	'@media (prefers-color-scheme:dark){:root:not([data-darkmode]) .ws{' + DARK + '}}',
	'.ws *{box-sizing:border-box}',
	'.ws small{color:inherit}',
	'.ws .num,.ws td,.ws .tab{font-variant-numeric:tabular-nums}',
	'.ws h2{margin-bottom:6px}',
	'.ws-sub{opacity:.68;margin:0 0 16px;font-size:13px}',
	'.ws-card{border:1px solid var(--ws-line);border-radius:10px;padding:14px 16px;background:var(--ws-fill);margin-bottom:14px;min-width:0}',
	'.ws-card>h3{margin:0 0 10px;font-size:15px;font-weight:600;border:0;padding:0;display:flex;align-items:baseline;gap:10px;flex-wrap:wrap}',
	'.ws-card>h3 small{font-weight:400;opacity:.75;font-size:12px;color:inherit}',
	'.ws-card>h3 .grow{flex:1}',
	'.ws-cols{display:grid;grid-template-columns:minmax(0,1.25fr) minmax(0,1fr);gap:14px}',
	'.ws-cols3{display:grid;grid-template-columns:repeat(auto-fit,minmax(260px,1fr));gap:14px}',
	'@media (max-width:900px){.ws-cols{grid-template-columns:minmax(0,1fr)}}',
	// the headline: what flows right now
	'.ws-now{display:flex;flex-wrap:wrap;align-items:flex-end;gap:10px 34px;margin-bottom:12px}',
	'.ws-now .big{font-size:34px;font-weight:650;line-height:1.05;letter-spacing:-.01em}',
	'.ws-now .big small{font-size:14px;font-weight:500;opacity:.7;margin-left:4px;letter-spacing:0}',
	'.ws-now .lab{font-size:12px;opacity:.7;margin-bottom:3px}',
	'.ws-now .side{font-size:13px;opacity:.85;line-height:1.6}',
	'.ws-now .side b{font-weight:600}',
	'.ws-live{display:inline-flex;align-items:center;gap:6px;font-size:12px;opacity:.8;font-weight:400}',
	'.ws-live i{width:8px;height:8px;border-radius:50%;background:var(--ws-good);display:inline-block}',
	'.ws-live.stale i{background:var(--ws-bad)}',
	// share of the pipe: one bar, a segment per uplink
	'.ws-share{display:flex;height:10px;border-radius:5px;overflow:hidden;background:var(--ws-track);gap:2px;margin:4px 0 6px}',
	'.ws-share span{display:block;height:100%;min-width:2px;transition:flex-grow .6s ease}',
	'.ws-legend{display:flex;flex-wrap:wrap;gap:4px 16px;font-size:12px;margin:6px 0 2px}',
	'.ws-legend span{display:inline-flex;align-items:center;gap:6px}',
	'.ws-legend b{font-weight:600}',
	'.ws-key{display:inline-block;width:10px;height:10px;border-radius:3px;flex:none}',
	'.ws-dot{display:inline-block;width:8px;height:8px;border-radius:50%;flex:none}',
	'.ws svg{display:block;overflow:visible}',
	'.ws svg.plot{width:100%;user-select:none;touch-action:pan-y}',
	'.ws-plot-label{font-size:12px;opacity:.75;margin:10px 0 2px;display:flex;justify-content:space-between}',
	// buttons and segmented controls
	'.ws .ws-btn{font:inherit;color:inherit;background:transparent;border:1px solid var(--ws-line);border-radius:6px;',
	'padding:3px 10px;margin:0;cursor:pointer;line-height:1.5;min-height:0;box-shadow:none;text-shadow:none;white-space:nowrap}',
	'.ws .ws-btn:hover{background:var(--ws-fill)}',
	'.ws .ws-btn:focus-visible{outline:2px solid var(--ws-c1);outline-offset:1px}',
	'.ws .ws-btn.on{background:var(--ws-c1);border-color:var(--ws-c1);color:#fff}',
	'.ws .ws-btn.bad{border-color:var(--ws-bad);color:var(--ws-bad)}',
	'.ws .ws-btn[disabled]{opacity:.55;cursor:wait}',
	'.ws-seg{display:inline-flex;flex-wrap:wrap;gap:4px}',
	'.ws-bar{display:flex;flex-wrap:wrap;gap:8px 14px;align-items:center;margin-bottom:12px}',
	'.ws-bar .grow{flex:1}',
	'.ws input.ws-in,.ws select.ws-in{font:inherit;padding:3px 8px;border:1px solid var(--ws-line);border-radius:6px;background:transparent;color:inherit;min-height:0;height:auto;width:auto}',
	'.ws select.ws-in,.ws select.ws-in option{background-color:Canvas;color:CanvasText}',
	// ranked rows with bars
	'.ws-rows{display:flex;flex-direction:column;gap:2px}',
	'.ws-row{display:grid;grid-template-columns:minmax(0,1fr) auto;gap:2px 12px;padding:6px 4px;border-radius:6px;align-items:center}',
	'.ws-row.click{cursor:pointer}.ws-row.click:hover{background:var(--ws-fill)}',
	'.ws-row .n{display:flex;align-items:center;gap:8px;min-width:0}',
	'.ws-row .n span.t{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:0 1 auto;min-width:3em}',
	'.ws-row .n small{opacity:.6;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}',
	'.ws-row .n .ws-chip{flex:none}',
	'.ws-row .v{text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}',
	'.ws-row .v small{opacity:.65;margin-left:6px}',
	'.ws-row .b{grid-column:1/3;display:flex;gap:2px;height:6px}',
	'.ws-row .b span{display:block;height:100%;border-radius:0 3px 3px 0;min-width:1px}',
	'.ws-row .b span.up{opacity:.45}',
	'.ws-chip{display:inline-block;font-size:11px;padding:0 7px;border-radius:9px;border:1px solid var(--ws-line);opacity:.85;white-space:nowrap;line-height:17px}',
	'.ws-chip.bad{border-color:var(--ws-bad);color:var(--ws-bad);opacity:1}',
	'.ws-chip.warn{border-color:var(--ws-warn);color:var(--ws-warn);opacity:1}',
	'.ws-chip.good{border-color:var(--ws-good);color:var(--ws-good);opacity:1}',
	// meters (cycle use against a cap)
	'.ws-meter{position:relative;height:10px;border-radius:5px;background:var(--ws-track);overflow:visible;margin:6px 0 4px}',
	'.ws-meter>span{position:absolute;left:0;top:0;bottom:0;border-radius:5px}',
	'.ws-meter>i{position:absolute;top:-3px;bottom:-3px;width:2px;background:currentColor;opacity:.55;border-radius:1px}',
	// tables
	'.ws table.ws-t{width:100%;border-collapse:collapse}',
	'.ws table.ws-t th{font-weight:600;font-size:12px;text-align:left;padding:6px 8px;border-bottom:1px solid var(--ws-line);white-space:nowrap;opacity:.8}',
	'.ws table.ws-t td{padding:6px 8px;border-bottom:1px solid var(--ws-line);vertical-align:middle}',
	'.ws table.ws-t tr.click{cursor:pointer}.ws table.ws-t tr.click:hover td{background:var(--ws-fill)}',
	'.ws table.ws-t td.r,.ws table.ws-t th.r{text-align:right;white-space:nowrap}',
	'.ws table.ws-t td small{opacity:.6}',
	'.ws-scroll{overflow-x:auto}',
	'.ws-empty{opacity:.65;padding:18px 4px;text-align:center}',
	'.ws-note{font-size:12px;opacity:.7;line-height:1.5}',
	'.ws-warnbox{border:1px solid var(--ws-warn);border-radius:8px;padding:8px 12px;margin-bottom:12px}',
	'.ws-infobox{border:1px solid var(--ws-c1);border-radius:8px;padding:8px 12px;margin-bottom:12px}',
	'.ws-tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:10px;margin-bottom:12px}',
	'.ws-tile{border:1px solid var(--ws-line);border-radius:8px;padding:8px 12px}',
	'.ws-tile .l{font-size:12px;opacity:.7}',
	'.ws-tile .v{font-size:20px;font-weight:600;line-height:1.3;font-variant-numeric:tabular-nums}',
	'.ws-tile .v small{font-size:12px;font-weight:400;opacity:.7;margin-left:3px}',
	'.ws-tile .d{font-size:12px;opacity:.7}',
	'.ws-tip{position:fixed;pointer-events:none;z-index:1000;padding:7px 10px;border-radius:7px;background:Canvas;color:CanvasText;',
	'box-shadow:0 4px 14px rgba(0,0,0,.28);border:1px solid var(--ws-line);white-space:nowrap;font-size:12px;font-variant-numeric:tabular-nums;line-height:1.6}',
	'.ws-tip .h{font-weight:600;margin-bottom:2px}',
	'.ws-tip .k{display:inline-block;width:10px;height:10px;border-radius:3px;margin-right:6px;vertical-align:-1px}',
	'.ws-tip .m{opacity:.65;margin-left:6px}',
	'.ws-back{display:inline-block;margin-bottom:10px;cursor:pointer;opacity:.8}',
	'.ws-kv{display:grid;grid-template-columns:auto minmax(0,1fr);gap:4px 14px;font-size:13px}',
	'.ws-kv>div:nth-child(odd){opacity:.65}'
].join('');

function S(tag, attrs, children) {
	var n = document.createElementNS(SVGNS, tag);
	for (var k in attrs)
		if (attrs[k] != null)
			n.setAttribute(k, attrs[k]);
	(children || []).forEach(function(c) { n.appendChild(typeof(c) == 'string' ? document.createTextNode(c) : c); });
	return n;
}

function pad(n) { return (n < 10 ? '0' : '') + n; }

function niceStep(v) {
	if (!(v > 0)) return 1;
	var p = Math.pow(10, Math.floor(Math.log10(v))), m = v / p;
	return (m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10) * p;
}

// numbers: speeds and amounts in bytes, decimal units (1 MB = 1,000,000 bytes, as carriers count)
function rateParts(Bps) {
	var b = Bps || 0;
	var u = b >= 1e9 ? [ 1e9, 'GB/s' ] : b >= 1e6 ? [ 1e6, 'MB/s' ] : b >= 1e3 ? [ 1e3, 'kB/s' ] : [ 1, 'B/s' ];
	var v = b / u[0];
	return [ u[0] == 1 ? String(Math.round(v)) : v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2).replace(/\.?0+$/, '') || '0', u[1] ];
}

function bytesParts(B) {
	B = B || 0;
	var u = B >= 1e12 ? [ 1e12, 'TB' ] : B >= 1e9 ? [ 1e9, 'GB' ] : B >= 1e6 ? [ 1e6, 'MB' ] : B >= 1e3 ? [ 1e3, 'kB' ] : [ 1, 'B' ];
	var v = B / u[0];
	return [ u[0] == 1 ? String(Math.round(v)) : v >= 100 ? v.toFixed(0) : v >= 10 ? v.toFixed(1) : v.toFixed(2), u[1] ];
}

function rate(Bps) { var p = rateParts(Bps); return p[0] + ' ' + p[1]; }
// a limit in kB/s (the unit the config keeps)
function limitText(kB) { return kB >= 1000 ? +(kB / 1000).toFixed(2) + ' MB/s' : kB + ' kB/s'; }
function bytes(B) { var p = bytesParts(B); return p[0] + ' ' + p[1]; }

function ago(ts, now) {
	var s = Math.max(0, (now || Date.now() / 1000) - ts);
	if (s < 60) return _('just now');
	if (s < 3600) return _('%d min ago').format(Math.floor(s / 60));
	if (s < 86400) return _('%d h ago').format(Math.floor(s / 3600));
	return _('%d days ago').format(Math.floor(s / 86400));
}

function dur(s) {
	s = Math.max(0, Math.round(s));
	if (s < 60) return s + ' s';
	if (s < 3600) return Math.floor(s / 60) + ' min';
	if (s < 86400) return Math.floor(s / 3600) + ' h ' + pad(Math.floor(s % 3600 / 60)) + ' min';
	return Math.floor(s / 86400) + ' d ' + Math.floor(s % 86400 / 3600) + ' h';
}

function hhmm(ts, secs) {
	var d = new Date(ts * 1000);
	return pad(d.getHours()) + ':' + pad(d.getMinutes()) + (secs ? ':' + pad(d.getSeconds()) : '');
}

function dayLabel(ts, withYear) {
	var d = new Date(ts * 1000);
	return d.toLocaleDateString(undefined, withYear ? { day: 'numeric', month: 'short', year: 'numeric' } : { weekday: 'short', day: 'numeric', month: 'short' });
}

// "d,u,d,u" -> { down: [], up: [] }
function unpack(csv) {
	var down = [], up = [];
	if (csv) {
		var a = csv.split(',');
		for (var i = 0; i + 1 < a.length; i += 2) { down.push(+a[i]); up.push(+a[i + 1]); }
	}
	return { down: down, up: up };
}

var devSlots = {}, devNext = 0;

// write calls answer { error } instead of failing, so LuCI keeps the message
function ok(r) { if (r && r.error) throw new Error(r.error); return r; }

var tipBox = null;
function tip(nodes, cx, cy) {
	if (!tipBox) {
		tipBox = E('div', { 'class': 'ws ws-tip', 'style': 'display:none' });
		document.body.appendChild(tipBox);
	}
	if (!nodes) { tipBox.style.display = 'none'; return; }
	tipBox.replaceChildren.apply(tipBox, nodes);
	tipBox.style.display = '';
	var w = tipBox.offsetWidth, h = tipBox.offsetHeight, left = cx + 16, top = cy + 12;
	if (left + w > window.innerWidth - 8) left = cx - w - 16;
	if (top + h > window.innerHeight - 8) top = window.innerHeight - h - 8;
	tipBox.style.left = Math.max(4, left) + 'px';
	tipBox.style.top = Math.max(4, top) + 'px';
}

return baseclass.extend({
	S: S,
	rate: rate,
	limitText: limitText,
	rateParts: rateParts,
	bytes: bytes,
	bytesParts: bytesParts,
	ago: ago,
	dur: dur,
	hhmm: hhmm,
	dayLabel: dayLabel,
	unpack: unpack,
	tip: tip,

	// a chart under the pointer: pages skip redrawing it so its tooltip stays put
	hovering: function() { return !!document.querySelector('.ws svg.plot:hover'); },

	status: function() { return L.resolveDefault(callStatus(), {}); },
	live: function(since, win, devs) { return L.resolveDefault(callLive(since || 0, win || 300, devs !== false), { ifaces: {}, devices: {} }); },
	devices: function() { return L.resolveDefault(callDevices(), { devices: [] }); },
	device: function(key, from, to) { return callDevice(key, from || 0, to || 0); },
	wans: function() { return L.resolveDefault(callWans(), { wans: [] }); },
	flows: function(dev, wan, limit) { return L.resolveDefault(callFlows(dev || '', wan || '', limit || 500), { flows: [] }); },
	history: function(keys, from, to, step) { return L.resolveDefault(callHistory(keys, from, to, step), { series: {} }); },
	top: function(groups, from, to, limit) { return L.resolveDefault(callTop(groups, from, to, limit || 50), { groups: {}, all: {} }); },
	usage: function(keys, from, to) { return L.resolveDefault(callUsage(keys, from, to), { totals: {}, names: {} }); },
	alerts: function(limit) { return L.resolveDefault(callAlerts(limit || 200), { alerts: [] }); },
	report: function(from, to) { return callReport(from, to); },
	notifyTest: function() { return callNotifyTest(); },
	setDevice: function(key, o) {
		return callSetDevice(key, o.name, o.icon, o.block, o.pause, o.down, o.up, o.schedules, o.quotas, o.approved).then(function(r) {
			if (r && r.error) throw new Error(r.error);
			return r;
		});
	},
	setWan: function(name, o) { return callSetWan(name, o.label, o.kind, o.hidden).then(ok); },
	plans: function() { return L.resolveDefault(callPlans(), { plans: [] }); },
	setPlan: function(o) { return callSetPlan(o.id, o.name, o.type, o.size_gb, o.ifaces, o.cycle_day, o.valid_days, o.used_gb).then(ok); },
	topup: function(id, size, mode) { return callTopup(id, String(size), mode).then(ok); },
	calibrate: function(id, left) { return callCalibrate(id, String(left)).then(ok); },
	deletePlan: function(id) { return callDeletePlan(id).then(ok); },
	setGuest: function(policy, down, up) { return callSetGuest(policy, down || 0, up || 0); },
	forget: function(key) { return callForget(key); },
	// every service block, with its addresses and drops (the Blocked page)
	blocks: function() { return L.resolveDefault(callBlocks(), { blocks: [] }); },
	// blocks a service's addresses on one device (device '' = every device), or lifts that block
	blockService: function(service, device, block) { return callBlockService(service, device || '', !!block).then(ok); },
	// names a service: o = { name, category, and one of domain, cidr, port }
	setRule: function(o) { return callSetRule(o.name, o.category || 'other', o.domain || '', o.cidr || '', o.port || '').then(ok); },
	// installed and latest versions (check: ask GitHub now), and the progress of an update
	update: function(check) { return callUpdate(!!check).then(ok); },
	// installs the latest release; wanscope restarts on the way
	updateStart: function() { return callUpdateStart().then(ok); },

	css: function() {
		if (!document.getElementById('ws-css'))
			document.head.appendChild(E('style', { 'id': 'ws-css' }, [ CSS ]));
	},

	// colour slots: uplinks by their order, devices by first appearance
	color: function(i) { return i < 0 ? 'var(--ws-other)' : 'var(--ws-c' + (i % 8 + 1) + ')'; },
	wanColors: function(names) {
		var m = {};
		names.forEach(function(n, i) { m[n] = 'var(--ws-c' + (i % 8 + 1) + ')'; });
		return m;
	},
	devColor: function(key) {
		if (!(key in devSlots)) devSlots[key] = devNext++;
		var s = devSlots[key];
		return s < 7 ? 'var(--ws-c' + (s + 1) + ')' : 'var(--ws-other)';
	},
	// device colour slots in a stable order for a chart: known keys keep theirs, the rest go to "other"
	devColorFixed: function(keys) {
		var self = this, m = {};
		keys.forEach(function(k) { m[k] = self.devColor(k); });
		return m;
	},

	// service categories in a fixed order (their colours never move)
	CATS: [ 'video', 'games', 'social', 'messaging', 'music', 'cloud', 'updates', 'work' ],
	catColor: function(c) { var i = this.CATS.indexOf(c); return i < 0 ? 'var(--ws-other)' : 'var(--ws-c' + (i + 1) + ')'; },

	// uplinks get the colour slots in their order; tunnels share the neutral colour
	linkColors: function(ifaces) {
		var m = {}, i = 0;
		(ifaces || []).forEach(function(f) {
			if (f.role != 'wan') return;
			m[f.name] = f.kind == 'uplink' ? 'var(--ws-c' + (i++ % 8 + 1) + ')' : 'var(--ws-other)';
		});
		return m;
	},

	// the emoji the user picked, else one guessed from the name
	icon: function(d) {
		if (d.icon && /[^\x00-\x7f]/.test(d.icon)) return d.icon;
		var n = ((d.icon || '') + ' ' + (d.name || '') + ' ' + (d.hostname || '')).toLowerCase();
		if (d.key == 'router') return '⌂';
		if (/iphone|android|phone|pixel|galaxy|redmi|xiaomi|oppo|huawei/.test(n)) return '📱';
		if (/ipad|tab/.test(n)) return '📲';
		if (/tv|chromecast|roku|fire|shield|bravia/.test(n)) return '📺';
		if (/playstation|ps5|ps4|xbox|switch|console/.test(n)) return '🎮';
		if (/pc|desktop|laptop|macbook|imac|win|thinkpad|notebook/.test(n)) return '💻';
		if (/cam|camera|doorbell/.test(n)) return '📷';
		if (/printer/.test(n)) return '🖨';
		if (/speaker|echo|homepod|nest/.test(n)) return '🔈';
		if (/modem/.test(n)) return '📡';
		return '●';
	},

	EMOJI: [ '📱', '💻', '🖥️', '📲', '⌚', '🎮', '🕹️', '📺', '🔈', '🎧', '📷', '🖨️', '📡', '🛰️', '🔌', '💡', '🌡️', '🔒', '🚪', '🏠',
		'🧑‍💻', '👨', '👩', '🧒', '👶', '👴', '👵', '🧑‍🎓', '🧑‍🍳', '🐶', '🐱', '🚗', '🤖', '👾', '⭐', '❤️', '🔥', '🌙', '☁️', '📦' ],

	/* Name and emoji of a device; onsave(changes) after a save. An empty name or the automatic icon goes back to
	 * what wanscope finds on its own (DHCP name, LAN name, address). */
	editName: function(d, onsave) {
		var self = this, pick = d.icon || '';
		var name = E('input', { 'class': 'cbi-input-text', 'value': d.custom_name || '', 'placeholder': d.auto_name || d.name, 'maxlength': 64, 'style': 'width:100%' });
		var custom = E('input', { 'class': 'cbi-input-text', 'value': pick, 'placeholder': _('or paste any emoji'), 'maxlength': 16, 'style': 'width:9em',
			'input': function(ev) { pick = ev.target.value.trim(); mark(); } });
		var cells = [];
		var mark = function() { cells.forEach(function(c) { c.style.outline = c.dataset.e == pick ? '2px solid var(--ws-c1)' : 'none'; }); };
		var grid = E('div', { 'style': 'display:flex;flex-wrap:wrap;gap:4px;margin:8px 0' }, [
			E('button', { 'class': 'ws-btn', 'type': 'button', 'data-e': '', 'title': _('Automatic'), 'style': 'min-width:5em', 'click': function() { pick = ''; custom.value = ''; mark(); } }, [ _('Automatic') ])
		].concat(this.EMOJI.map(function(e) {
			return E('button', { 'class': 'ws-btn', 'type': 'button', 'data-e': e, 'style': 'font-size:20px;width:42px;height:40px;padding:0', 'aria-label': e,
				'click': function() { pick = e; custom.value = e; mark(); } }, [ e ]);
		})));
		cells = [].slice.call(grid.children);
		mark();
		var save = function() {
			ui.hideModal();
			self.setDevice(d.key, { name: name.value.trim(), icon: pick }).then(function() {
				ui.addNotification(null, E('p', {}, [ _('Saved.') ]), 'info');
				if (onsave) onsave();
			}).catch(function(e) { ui.addNotification(null, E('p', {}, [ _('Not saved: %s').format(e.message) ]), 'error'); });
		};
		name.addEventListener('keydown', function(ev) { if (ev.key == 'Enter') save(); });
		ui.showModal(_('Name and icon'), [
			E('div', { 'class': 'ws' }, [
				E('div', { 'class': 'ws-plot-label' }, [ _('Name') ]), name,
				E('div', { 'class': 'ws-note', 'style': 'margin-top:4px' }, [ _('Leave empty to use the name the device gives itself.') ]),
				E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:14px' }, [ _('Icon') ]), grid, custom ]),
			E('div', { 'class': 'right', 'style': 'margin-top:14px' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': save }, [ _('Save') ]) ])
		]);
		name.focus();
	},

	tile: function(label, value, unit, desc) {
		return E('div', { 'class': 'ws-tile' }, [ E('div', { 'class': 'l' }, [ label ]),
			E('div', { 'class': 'v' }, [ value, unit ? E('small', {}, [ unit ]) : '' ]), desc ? E('div', { 'class': 'd' }, [ desc ]) : '' ]);
	},

	seg: function(options, cur, onpick) {
		return E('div', { 'class': 'ws-seg', 'role': 'group' }, options.map(function(o) {
			return E('button', { 'class': 'ws-btn' + (String(o[0]) == String(cur) ? ' on' : ''), 'type': 'button',
				'aria-pressed': String(o[0]) == String(cur) ? 'true' : 'false', 'click': function() { onpick(o[0]); } }, [ o[1] ]);
		}));
	},

	liveDot: function(ok) {
		return E('span', { 'class': 'ws-live' + (ok ? '' : ' stale') }, [ E('i'), ok ? _('live') : _('no answer from the router') ]);
	},

	/* Stacked area chart over time, one layer per series; hover shows every layer at that moment.
	 * sp: { W, H, t0, step, n, layers: [{ label, color, vals: [] }], fmt(v), fmtTime(ts), title } */
	areas: function(sp) {
		var W = sp.W, H = sp.H, L = 54, R = 8, top = 10, bottom = sp.noAxis ? 6 : 22, n = sp.n;
		var t1 = sp.t0 + (n - 1) * sp.step;
		var x = function(i) { return L + (n <= 1 ? 0 : i / (n - 1)) * (W - L - R); };
		var sums = new Array(n).fill(0);
		var stack = sp.layers.map(function(l) {
			return l.vals.map(function(v, i) { sums[i] += v || 0; return sums[i]; });
		});
		var vmax = Math.max.apply(null, sums.concat([ sp.minMax || 1 ]));
		var step = niceStep(vmax / 3), y1 = Math.ceil(vmax / step) * step || step;
		var y = function(v) { return H - bottom - Math.min(v, y1) / y1 * (H - bottom - top); };
		var svg = S('svg', { 'class': 'plot', 'height': H, 'viewBox': '0 0 ' + W + ' ' + H, 'preserveAspectRatio': 'none', 'role': 'img',
			'aria-label': sp.title || '' });
		for (var v = 0; v <= y1 + 1e-9; v += step) {
			svg.appendChild(S('line', { 'x1': L, 'x2': W - R, 'y1': y(v), 'y2': y(v), 'stroke': v == 0 ? 'var(--ws-axis)' : 'var(--ws-grid)', 'shape-rendering': 'crispEdges' }));
			svg.appendChild(S('text', { 'x': L - 8, 'y': y(v) + 4, 'text-anchor': 'end', 'fill': 'currentColor', 'fill-opacity': 0.6, 'font-size': 11 }, [ sp.fmt(v) ]));
		}
		if (!sp.noAxis) {
			var span = t1 - sp.t0, tick = [ 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600 ].filter(function(s) { return span / s <= 6; })[0] || 21600;
			for (var t = Math.ceil(sp.t0 / tick) * tick; t <= t1; t += tick) {
				var xx = x((t - sp.t0) / sp.step);
				svg.appendChild(S('line', { 'x1': xx, 'x2': xx, 'y1': H - bottom, 'y2': H - bottom + 4, 'stroke': 'var(--ws-axis)' }));
				svg.appendChild(S('text', { 'x': xx, 'y': H - 5, 'text-anchor': 'middle', 'fill': 'currentColor', 'fill-opacity': 0.6, 'font-size': 11 },
					[ (sp.fmtTime || hhmm)(t) ]));
			}
		}
		// layers from the top of the stack down, each filled to the baseline; a thin surface line between layers
		for (var li = stack.length - 1; li >= 0; li--) {
			var pts = stack[li], d = '';
			for (var i = 0; i < n; i++) d += (i ? 'L' : 'M') + x(i).toFixed(1) + ',' + y(pts[i]).toFixed(1);
			if (n > 1) {
				svg.appendChild(S('path', { 'd': d + 'L' + x(n - 1).toFixed(1) + ',' + y(0) + 'L' + x(0).toFixed(1) + ',' + y(0) + 'Z',
					'fill': sp.layers[li].color, 'fill-opacity': 0.85, 'stroke': 'none' }));
				svg.appendChild(S('path', { 'd': d, 'fill': 'none', 'stroke': 'var(--ws-surface, Canvas)', 'stroke-width': 1, 'stroke-opacity': 0.9 }));
			}
		}
		var cross = S('line', { 'y1': top, 'y2': H - bottom, 'stroke': 'currentColor', 'stroke-opacity': 0.55, 'visibility': 'hidden', 'pointer-events': 'none' });
		svg.appendChild(cross);
		var hit = S('rect', { 'x': L, 'y': 0, 'width': Math.max(0, W - L - R), 'height': H, 'fill': 'transparent' });
		svg.appendChild(hit);
		var at = function(ev) {
			var r = svg.getBoundingClientRect(), vx = (ev.clientX - r.left) * W / r.width;
			return Math.max(0, Math.min(n - 1, Math.round((vx - L) / (W - L - R) * (n - 1))));
		};
		var chart = {
			svg: svg,
			show: function(i, cx, cy) {
				cross.setAttribute('x1', x(i)); cross.setAttribute('x2', x(i)); cross.setAttribute('visibility', 'visible');
				if (cx == null) return;
				var rows = [ E('div', { 'class': 'h' }, [ (sp.fmtTime || function(t) { return hhmm(t, sp.step < 60); })(sp.t0 + i * sp.step) + (sp.title ? ' · ' + sp.title : '') ]) ];
				var tot = 0;
				for (var k = sp.layers.length - 1; k >= 0; k--) {
					var l = sp.layers[k], v = l.vals[i] || 0;
					tot += v;
					if (v > 0 || sp.layers.length <= 4)
						rows.push(E('div', {}, [ E('span', { 'class': 'k', 'style': 'background:' + l.color }), sp.fmt(v, true), E('span', { 'class': 'm' }, [ l.label ]) ]));
				}
				if (sp.layers.length > 1) rows.push(E('div', {}, [ E('b', {}, [ sp.fmt(tot, true) ]), E('span', { 'class': 'm' }, [ _('in all') ]) ]));
				tip(rows, cx, cy);
			},
			hide: function() { cross.setAttribute('visibility', 'hidden'); tip(null); }
		};
		svg.addEventListener('pointermove', function(ev) { var i = at(ev); chart.show(i, ev.clientX, ev.clientY); if (sp.onHover) sp.onHover(i); });
		svg.addEventListener('pointerleave', function() { chart.hide(); if (sp.onHover) sp.onHover(null); });
		return chart;
	},

	/* Stacked bars per bin (history). sp: { W, H, bins: [ts], layers: [{ label, color, vals }], fmt(v), fmtBin(ts), onPick(i) } */
	bars: function(sp) {
		var W = sp.W, H = sp.H, L = 54, R = 6, top = 10, bottom = 22, n = sp.bins.length;
		var sums = new Array(n).fill(0);
		sp.layers.forEach(function(l) { l.vals.forEach(function(v, i) { sums[i] += v || 0; }); });
		var vmax = Math.max.apply(null, sums.concat([ 1 ]));
		var step = niceStep(vmax / 3), y1 = Math.ceil(vmax / step) * step || step;
		var y = function(v) { return H - bottom - Math.min(v, y1) / y1 * (H - bottom - top); };
		var bw = (W - L - R) / Math.max(n, 1), gap = Math.min(4, bw * 0.25), w = Math.max(1, bw - gap);
		var svg = S('svg', { 'class': 'plot', 'height': H, 'viewBox': '0 0 ' + W + ' ' + H, 'preserveAspectRatio': 'none', 'role': 'img' });
		for (var v = 0; v <= y1 + 1e-9; v += step) {
			svg.appendChild(S('line', { 'x1': L, 'x2': W - R, 'y1': y(v), 'y2': y(v), 'stroke': v == 0 ? 'var(--ws-axis)' : 'var(--ws-grid)', 'shape-rendering': 'crispEdges' }));
			svg.appendChild(S('text', { 'x': L - 8, 'y': y(v) + 4, 'text-anchor': 'end', 'fill': 'currentColor', 'fill-opacity': 0.6, 'font-size': 11 }, [ sp.fmt(v) ]));
		}
		var every = Math.max(1, Math.ceil(n / Math.max(1, Math.floor((W - L) / 64))));
		var hl = S('rect', { 'y': top, 'height': H - bottom - top, 'width': bw, 'fill': 'currentColor', 'fill-opacity': 0.06, 'visibility': 'hidden', 'pointer-events': 'none' });
		svg.appendChild(hl);
		for (var i = 0; i < n; i++) {
			var x0 = L + i * bw + gap / 2, base = 0;
			sp.layers.forEach(function(l, li) {
				var v = l.vals[i] || 0;
				if (v <= 0) return;
				var ya = y(base + v), yb = y(base), hgt = Math.max(1, yb - ya - (base > 0 ? 1 : 0));
				var last = sp.layers.slice(li + 1).every(function(o) { return !(o.vals[i] > 0); });
				// rounded top on the last segment, square elsewhere; 1px surface gap between segments
				var r = last ? Math.min(3, w / 2, hgt) : 0;
				var d = 'M' + x0 + ',' + yb + 'V' + (ya + r) + (r ? 'Q' + x0 + ',' + ya + ' ' + (x0 + r) + ',' + ya + 'H' + (x0 + w - r) + 'Q' + (x0 + w) + ',' + ya + ' ' + (x0 + w) + ',' + (ya + r) : 'H' + (x0 + w)) + 'V' + yb + 'Z';
				svg.appendChild(S('path', { 'd': d, 'fill': l.color }));
				base += v;
			});
			if (i % every == 0)
				svg.appendChild(S('text', { 'x': L + i * bw + bw / 2, 'y': H - 5, 'text-anchor': 'middle', 'fill': 'currentColor', 'fill-opacity': 0.6, 'font-size': 11 }, [ sp.fmtBin(sp.bins[i]) ]));
		}
		var hit = S('rect', { 'x': L, 'y': 0, 'width': Math.max(0, W - L - R), 'height': H, 'fill': 'transparent', 'style': sp.onPick ? 'cursor:pointer' : null });
		svg.appendChild(hit);
		var at = function(ev) {
			var r = svg.getBoundingClientRect(), vx = (ev.clientX - r.left) * W / r.width;
			return Math.max(0, Math.min(n - 1, Math.floor((vx - L) / bw)));
		};
		svg.addEventListener('pointermove', function(ev) {
			var i = at(ev);
			hl.setAttribute('x', L + i * bw); hl.setAttribute('visibility', 'visible');
			var rows = [ E('div', { 'class': 'h' }, [ (sp.fmtTip || sp.fmtBin)(sp.bins[i]) ]) ], tot = 0;
			for (var k = sp.layers.length - 1; k >= 0; k--) {
				var l = sp.layers[k], v = l.vals[i] || 0;
				tot += v;
				if (v > 0) rows.push(E('div', {}, [ E('span', { 'class': 'k', 'style': 'background:' + l.color }), sp.fmt(v, true), E('span', { 'class': 'm' }, [ l.label ]) ]));
			}
			if (sp.layers.length > 1) rows.push(E('div', {}, [ E('b', {}, [ sp.fmt(tot, true) ]), E('span', { 'class': 'm' }, [ _('in all') ]) ]));
			tip(rows, ev.clientX, ev.clientY);
		});
		svg.addEventListener('pointerleave', function() { hl.setAttribute('visibility', 'hidden'); tip(null); });
		if (sp.onPick) svg.addEventListener('click', function(ev) { sp.onPick(at(ev)); });
		return svg;
	},

	/* tiny line of recent values */
	spark: function(vals, color, w, h) {
		w = w || 90; h = h || 22;
		var m = Math.max.apply(null, vals.concat([ 1 ])), n = vals.length;
		var d = vals.map(function(v, i) { return (i ? 'L' : 'M') + (n <= 1 ? 0 : i / (n - 1) * w).toFixed(1) + ',' + (h - 1 - v / m * (h - 3)).toFixed(1); }).join('');
		return S('svg', { 'width': w, 'height': h, 'viewBox': '0 0 ' + w + ' ' + h, 'aria-hidden': 'true' }, [
			S('path', { 'd': d + 'L' + w + ',' + h + 'L0,' + h + 'Z', 'fill': color, 'fill-opacity': 0.15 }),
			S('path', { 'd': d, 'fill': 'none', 'stroke': color, 'stroke-width': 1.5, 'stroke-linejoin': 'round' }) ]);
	},

	/* ranked rows: [{ label, sub, color, down, up, value (text), click }] with bars scaled to the largest total */
	rows: function(list, opts) {
		opts = opts || {};
		var max = Math.max.apply(null, list.map(function(r) { return (r.down || 0) + (r.up || 0); }).concat([ 1 ]));
		if (!list.length) return E('div', { 'class': 'ws-empty' }, [ opts.empty || _('Nothing yet.') ]);
		return E('div', { 'class': 'ws-rows' }, list.map(function(r) {
			var dw = (r.down || 0) / max * 100, uw = (r.up || 0) / max * 100;
			return E('div', { 'class': 'ws-row' + (r.click ? ' click' : ''), 'click': r.click || null, 'tabindex': r.click ? 0 : null,
				'keydown': r.click ? function(ev) { if (ev.key == 'Enter') r.click(); } : null }, [
				E('div', { 'class': 'n' }, [ r.color ? E('span', { 'class': 'ws-key', 'style': 'background:' + r.color }) : '',
					r.icon ? E('span', {}, [ r.icon ]) : '', E('span', { 'class': 't', 'title': r.title || r.label }, [ r.label ]),
					r.sub ? E('small', {}, [ r.sub ]) : '' ].concat(r.chips || [])),
				E('div', { 'class': 'v' }, [ r.value || bytes((r.down || 0) + (r.up || 0)), r.vsub ? E('small', {}, [ r.vsub ]) : '' ]),
				E('div', { 'class': 'b', 'title': _('download %s, upload %s').format(opts.rate ? rate(r.down) : bytes(r.down), opts.rate ? rate(r.up) : bytes(r.up)) }, [
					E('span', { 'style': 'width:' + dw.toFixed(2) + '%;background:' + (r.color || 'var(--ws-c1)') }),
					E('span', { 'class': 'up', 'style': 'width:' + uw.toFixed(2) + '%;background:' + (r.color || 'var(--ws-c1)') }) ])
			]);
		}));
	},

	/* use of a cap: used, forecast tick; colour turns to warning above 80 %, bad above 100 % */
	meter: function(used, cap, forecast, color) {
		var p = cap > 0 ? used / cap : 0;
		var c = p >= 1 ? 'var(--ws-bad)' : p >= 0.8 ? 'var(--ws-warn)' : (color || 'var(--ws-c1)');
		var kids = [ E('span', { 'style': 'width:' + Math.min(100, p * 100).toFixed(2) + '%;background:' + c }) ];
		if (cap > 0 && forecast > 0)
			kids.push(E('i', { 'style': 'left:calc(' + Math.min(100, forecast / cap * 100).toFixed(2) + '% - 1px)', 'title': _('forecast for the cycle: %s').format(bytes(forecast)) }));
		return E('div', { 'class': 'ws-meter', 'role': 'meter', 'aria-valuenow': Math.round(p * 100), 'aria-valuemin': 0, 'aria-valuemax': 100 }, kids);
	},

	chips: function(d) {
		var out = [], e = d.effect || {};
		if (e.blocked) out.push(E('span', { 'class': 'ws-chip bad', 'title': (e.reasons || []).join(', ') }, [ e.until ? _('paused') : e.guest ? _('waiting for approval') : _('blocked') ]));
		else if (e.down || e.up) out.push(E('span', { 'class': 'ws-chip warn', 'title': (e.reasons || []).join(', ') }, [ _('limited') ]));
		if ((e.quota_hit || []).length && !e.blocked && !(e.down || e.up)) out.push(E('span', { 'class': 'ws-chip warn' }, [ _('quota used') ]));
		return out;
	},

	plotWidth: function(node, fallback) {
		var w = node && node.clientWidth;
		return w > 100 ? w : (fallback || 900);
	}
});
