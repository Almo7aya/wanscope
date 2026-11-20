'use strict';
'require view';
'require poll';
'require wanscope.ui as ws';

// Network > wanscope > History: any range, by uplink or by device, with what it was used for; CSV export.

var PRESETS = [ [ 'today', _('Today') ], [ 'yesterday', _('Yesterday') ], [ '7d', _('7 days') ], [ 'month', _('This month') ], [ '30d', _('30 days') ], [ '12m', _('12 months') ] ];

function midnight(d) { d = new Date(d); d.setHours(0, 0, 0, 0); return d; }
function secs(d) { return Math.floor(d / 1000); }

function span(preset, a, b) {
	var now = new Date(), today = midnight(now), r = {};
	switch (preset) {
	case 'yesterday': r.from = new Date(today); r.from.setDate(r.from.getDate() - 1); r.to = today; break;
	case '7d': r.from = new Date(today); r.from.setDate(r.from.getDate() - 6); r.to = now; break;
	case 'month': r.from = new Date(today); r.from.setDate(1); r.to = now; break;
	case '30d': r.from = new Date(today); r.from.setDate(r.from.getDate() - 29); r.to = now; break;
	case '12m': r.from = new Date(today); r.from.setDate(1); r.from.setMonth(r.from.getMonth() - 11); r.to = now; break;
	case 'custom': r.from = midnight(a); r.to = new Date(midnight(b).getTime() + 86400000); break;
	default: r.from = today; r.to = now;
	}
	var len = (r.to - r.from) / 1000;
	r.step = len <= 90000 ? 600 : len <= 2 * 86400 ? 1800 : len <= 92 * 86400 ? 86400 : -1;
	r.fmt = r.step < 86400 && r.step > 0 ? function(t) { return ws.hhmm(t); } : r.step == 86400 ? function(t) { return String(new Date(t * 1000).getDate()); }
		: function(t) { return new Date(t * 1000).toLocaleDateString(undefined, { month: 'short' }); };
	r.tip = r.step < 86400 && r.step > 0 ? function(t) { return ws.dayLabel(t) + ' ' + ws.hhmm(t) + '–' + ws.hhmm(t + r.step); }
		: r.step == 86400 ? function(t) { return ws.dayLabel(t); } : function(t) { return new Date(t * 1000).toLocaleDateString(undefined, { month: 'long', year: 'numeric' }); };
	return r;
}

function isoDay(d) { d = new Date(d); return d.getFullYear() + '-' + ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2); }

return view.extend({
	load: function() {
		return Promise.all([ ws.status(), ws.devices() ]);
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.st = res[0] || {};
		this.devs = res[1].devices || [];
		this.colors = ws.linkColors(this.st.ifaces);
		var weekAgo = new Date(); weekAgo.setDate(weekAgo.getDate() - 6);
		this.v = { preset: 'today', by: 'uplinks', dir: 'total', a: isoDay(weekAgo), b: isoDay(new Date()) };
		this.box = { bar: E('div', { 'class': 'ws-bar' }), chart: E('div'), legend: E('div'), table: E('div'), svc: E('div'), sites: E('div'), title: E('span') };
		var b = this.box;
		this.root = E('div', { 'class': 'ws' }, [
			E('h2', {}, [ _('History') ]),
			E('p', { 'class': 'ws-sub' }, [ _('Data over any range: per uplink (counted on the link) or per device (counted per connection).') ]),
			E('div', { 'class': 'ws-card' }, [ b.bar, E('h3', {}, [ b.title ]), b.chart, b.legend ]),
			E('div', { 'class': 'ws-card' }, [ b.table ]),
			E('div', { 'class': 'ws-cols' }, [
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Services') ]), b.svc ]),
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Sites') ]), b.sites ]) ])
		]);
		this.drawBar();
		requestAnimationFrame(function() { self.load2(); });
		// a range that runs up to now keeps filling in
		poll.add(function() { return self.r && self.r.to >= Date.now() - 60000 ? self.load2() : Promise.resolve(); }, 5);
		return this.root;
	},

	label: function(name) {
		var i = (this.st.ifaces || []).filter(function(x) { return x.name == name; })[0];
		return i ? i.label : name;
	},

	drawBar: function() {
		var self = this, v = this.v;
		var a = E('input', { 'class': 'ws-in', 'type': 'date', 'value': v.a, 'change': function(ev) { v.a = ev.target.value; v.preset = 'custom'; self.drawBar(); self.load2(); } });
		var b = E('input', { 'class': 'ws-in', 'type': 'date', 'value': v.b, 'change': function(ev) { v.b = ev.target.value; v.preset = 'custom'; self.drawBar(); self.load2(); } });
		this.box.bar.replaceChildren(
			ws.seg(PRESETS, v.preset, function(p) { v.preset = p; self.drawBar(); self.load2(); }),
			E('span', { 'class': 'ws-note' }, [ _('or') ]), a, '–', b,
			E('span', { 'class': 'grow' }),
			ws.seg([ [ 'uplinks', _('By uplink') ], [ 'devices', _('By device') ] ], v.by, function(x) { v.by = x; self.drawBar(); self.load2(); }),
			ws.seg([ [ 'total', _('Both ways') ], [ 'down', _('Download') ], [ 'up', _('Upload') ] ], v.dir, function(x) { v.dir = x; self.drawBar(); self.draw(); })
		);
	},

	load2: function() {
		var self = this, v = this.v, r = span(v.preset, v.a, v.b);
		this.r = r;
		var keys = v.by == 'uplinks' ? [ 'i:*' ] : [ 'd:*' ];
		var from = secs(r.from), to = secs(r.to) + 1;
		this.box.title.replaceChildren(r.from.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: v.preset == 'today' || v.preset == 'yesterday' ? 'short' : undefined }) +
			' – ' + r.to.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' }));
		return Promise.all([ ws.history(keys, from, to, r.step), ws.usage(keys, from, to), ws.top([ '*' ], from, to, 15) ]).then(function(res) {
			self.hist = res[0]; self.usage = res[1]; self.top = res[2];
			self.draw();
		});
	},

	draw: function() {
		var self = this, v = this.v, r = this.r, h = this.hist || {}, b = this.box;
		var pick = function(p) { return v.dir == 'down' ? p.down : v.dir == 'up' ? p.up : p.down.map(function(x, i) { return x + p.up[i]; }); };
		var bins = (h.bins || '').split(',').filter(Boolean).map(Number);
		var ifaces = {};
		(this.st.ifaces || []).forEach(function(i) { ifaces[i.name] = i; });
		var names = (this.usage || {}).names || {};
		var tot = (this.usage || {}).totals || {};
		var rows = Object.keys(tot).map(function(k) {
			var id = k.slice(2), c = tot[k];
			return { key: k, id: id, down: c.down, up: c.up, name: v.by == 'uplinks' ? self.label(id) : (names[id] || id.replace(/^ip:/, '')) };
		}).filter(function(x) {
			if (v.by != 'uplinks') return true;
			var i = ifaces[x.id];
			return i && i.role == 'wan';
		}).sort(function(a, b) { return b.down + b.up - (a.down + a.up); });

		var layers;
		if (v.by == 'uplinks') {
			layers = rows.filter(function(x) { return ifaces[x.id].kind == 'uplink' && h.series && h.series[x.key]; }).map(function(x) {
				return { label: x.name, color: self.colors[x.id], vals: pick(ws.unpack(h.series[x.key])) };
			});
		} else {
			var top = rows.slice(0, 7), rest = rows.slice(7);
			layers = top.filter(function(x) { return h.series && h.series[x.key]; }).map(function(x) {
				return { label: x.name, color: ws.devColor(x.id), vals: pick(ws.unpack(h.series[x.key])) };
			});
			if (rest.length) {
				var other = new Array(bins.length).fill(0);
				rest.forEach(function(x) { if (h.series && h.series[x.key]) pick(ws.unpack(h.series[x.key])).forEach(function(val, i) { other[i] += val; }); });
				layers.push({ label: _('%d other devices').format(rest.length), color: 'var(--ws-other)', vals: other });
			}
		}
		if (!ws.hovering()) b.chart.replaceChildren(bins.length && layers.length ? ws.bars({ W: ws.plotWidth(b.chart), H: 240, bins: bins, layers: layers,
			fmt: function(x) { return ws.bytes(x); }, fmtBin: r.fmt, fmtTip: r.tip }) : E('div', { 'class': 'ws-empty' }, [ _('No data for this range yet.') ]));
		b.legend.replaceChildren(E('div', { 'class': 'ws-legend' }, layers.map(function(l) {
			return E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:' + l.color }), l.label, E('b', {}, [ ws.bytes(l.vals.reduce(function(s, x) { return s + x; }, 0)) ]) ]);
		})));

		var sum = rows.filter(function(x) { return v.by != 'uplinks' || ifaces[x.id].kind == 'uplink'; }).reduce(function(s, x) { return s + x.down + x.up; }, 0);
		var csv = function() {
			var lines = [ [ v.by == 'uplinks' ? 'uplink' : 'device', 'key', 'download_bytes', 'upload_bytes', 'total_bytes' ].join(',') ];
			rows.forEach(function(x) { lines.push([ '"' + x.name.replace(/"/g, '""') + '"', x.id, x.down, x.up, x.down + x.up ].join(',')); });
			var a = E('a', { 'href': URL.createObjectURL(new Blob([ lines.join('\n') + '\n' ], { type: 'text/csv' })), 'download': 'wanscope-' + v.by + '-' + isoDay(r.from) + '.csv' });
			document.body.appendChild(a); a.click(); a.remove();
		};
		b.table.replaceChildren(
			E('div', { 'class': 'ws-bar' }, [ E('b', {}, [ v.by == 'uplinks' ? _('Uplinks') : _('Devices') ]), E('span', { 'class': 'grow' }),
				E('button', { 'class': 'ws-btn', 'click': csv }, [ _('Download CSV') ]) ]),
			rows.length ? E('table', { 'class': 'ws-t' }, [
				E('tr', {}, [ E('th', {}, [ v.by == 'uplinks' ? _('Uplink') : _('Device') ]), E('th', { 'class': 'r' }, [ _('Download') ]),
					E('th', { 'class': 'r' }, [ _('Upload') ]), E('th', { 'class': 'r' }, [ _('In all') ]), E('th', { 'class': 'r' }, [ _('Share') ]) ])
			].concat(rows.map(function(x) {
				var tunnel = v.by == 'uplinks' && ifaces[x.id].kind == 'tunnel';
				return E('tr', { 'class': v.by == 'devices' ? 'click' : '', 'click': v.by == 'devices' ? function() { location.href = L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(x.id); } : null }, [
					E('td', {}, [ E('span', { 'class': 'ws-key', 'style': 'margin-right:8px;background:' + (v.by == 'uplinks' ? self.colors[x.id] : ws.devColor(x.id)) }), x.name,
						tunnel ? E('small', {}, [ ' · ', _('tunnel, carried by the uplinks above') ]) : '' ]),
					E('td', { 'class': 'r' }, [ ws.bytes(x.down) ]), E('td', { 'class': 'r' }, [ ws.bytes(x.up) ]),
					E('td', { 'class': 'r' }, [ E('b', {}, [ ws.bytes(x.down + x.up) ]) ]),
					E('td', { 'class': 'r' }, [ tunnel || !sum ? '' : Math.round((x.down + x.up) / sum * 100) + '%' ])
				]);
			}))) : E('div', { 'class': 'ws-empty' }, [ _('Nothing in this range.') ]));

		var all = (this.top || {}).all || {};
		b.svc.replaceChildren(ws.rows((all.services || []).map(function(s) { return { label: s.name, sub: s.category != 'other' ? s.category : '', down: s.down, up: s.up, color: ws.catColor(s.category) }; })));
		b.sites.replaceChildren(ws.rows((all.sites || []).map(function(s) { return { label: s.name, down: s.down, up: s.up, color: 'var(--ws-c1)' }; })));
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
