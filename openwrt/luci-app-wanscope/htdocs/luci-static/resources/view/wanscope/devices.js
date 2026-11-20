'use strict';
'require view';
'require poll';
'require ui';
'require wanscope.ui as ws';

// Network > wanscope > Devices: every device with its traffic and controls; #<key> opens one device.

var DAYS = [ [ 'mon', _('Mon') ], [ 'tue', _('Tue') ], [ 'wed', _('Wed') ], [ 'thu', _('Thu') ], [ 'fri', _('Fri') ], [ 'sat', _('Sat') ], [ 'sun', _('Sun') ] ];
var RANGES = [ [ 'today', _('Today') ], [ '24h', _('24 h') ], [ '7d', _('7 days') ], [ '30d', _('30 days') ], [ '12m', _('12 months') ] ];

function range(r) {
	var now = Math.floor(Date.now() / 1000), d = new Date(); d.setHours(0, 0, 0, 0);
	var day = Math.floor(d / 1000);
	switch (r) {
	case '24h': return { from: now - 86400, to: now + 1, step: 3600, fmt: function(t) { return ws.hhmm(t); } };
	case '7d': return { from: day - 6 * 86400, to: now + 1, step: 21600, fmt: function(t) { return ws.dayLabel(t).replace(/,.*/, '') + ' ' + ws.hhmm(t); } };
	case '30d': return { from: day - 29 * 86400, to: now + 1, step: 86400, fmt: function(t) { return new Date(t * 1000).getDate() + ''; }, tip: function(t) { return ws.dayLabel(t); } };
	case '12m':
		var m = new Date(); m.setDate(1); m.setHours(0, 0, 0, 0); m.setMonth(m.getMonth() - 11);
		return { from: Math.floor(m / 1000), to: now + 1, step: -1, fmt: function(t) { return new Date(t * 1000).toLocaleDateString(undefined, { month: 'short' }); },
			tip: function(t) { return new Date(t * 1000).toLocaleDateString(undefined, { month: 'long', year: 'numeric' }); } };
	}
	return { from: day, to: now + 1, step: 3600, fmt: function(t) { return ws.hhmm(t); } };
}

function kbit(v) { return ws.limitText(v); }
// limits are kept in kB/s and typed in MB/s
function toMB(kB) { return kB ? +(kB / 1000).toFixed(3) : ''; }
function toKB(mb) { return Math.max(0, Math.round((+mb || 0) * 1000)); }

function describeSchedule(s) {
	var f = s.split(/\s+/);
	var days = f[0] == 'daily' ? _('every day') : f[0] == 'weekdays' ? _('weekdays') : f[0] == 'weekend' ? _('weekends') : f[0].split(',').map(function(d) {
		var x = DAYS.filter(function(k) { return k[0] == d; })[0]; return x ? x[1] : d; }).join(', ');
	var what = f[2] == 'block' ? _('no internet') : _('limit to %s down, %s up').format(kbit(+f[3] || 0), f[4] ? kbit(+f[4]) : _('no limit'));
	return days + ' ' + f[1].replace('-', '–') + ': ' + what;
}

function describeQuota(q) {
	var f = q.split(/\s+/);
	var per = { day: _('a day'), week: _('a week'), month: _('a month') }[f[0]] || f[0];
	var dir = { total: _('in all'), down: _('download'), up: _('upload') }[f[1]] || f[1];
	var mb = +f[2], size = mb >= 1000 ? (mb / 1000) + ' GB' : mb + ' MB';
	var act = f[3] == 'block' ? _('then no internet') : f[3] == 'limit' ? _('then limit to %s').format(kbit(+f[4] || 0)) : _('then an alert');
	return size + ' ' + dir + ' ' + per + ', ' + act;
}

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
		this.root = E('div', { 'class': 'ws' });
		this.view = { sort: 'today', q: '', older: false, range: 'today' };
		window.addEventListener('hashchange', function() { self.route(); });
		this.route();
		poll.add(function() { return self.refresh(); }, 1);
		return this.root;
	},

	route: function() {
		var key = decodeURIComponent(location.hash.replace(/^#/, ''));
		this.cur = key || null;
		this.detail = null;
		this.liveBuf = null;
		if (key) this.renderDetail(); else this.renderList();
	},

	refresh: function() {
		var self = this;
		if (this.cur) return this.refreshDetail();
		return ws.devices().then(function(r) { self.devs = r.devices || self.devs; self.drawList(); });
	},

	label: function(name) {
		var i = (this.st.ifaces || []).filter(function(x) { return x.name == name; })[0];
		return i ? i.label : name;
	},

	// ---------- the list ----------
	// The order is set when the page opens and when a column header is clicked; refreshes only update the numbers,
	// so rows never jump around. Offline devices stay in place, dimmed.
	renderList: function() {
		var self = this;
		this.listBox = E('div', { 'class': 'ws-scroll' });
		this.sumBox = E('span', { 'class': 'ws-note' });
		this.waitBox = E('div');
		this.order = null;
		var search = E('input', { 'class': 'ws-in', 'type': 'search', 'placeholder': _('Find a device, IP or MAC'), 'value': this.view.q, 'style': 'min-width:240px',
			'input': function(ev) { self.view.q = ev.target.value.toLowerCase(); self.drawList(); } });
		var older = E('label', { 'class': 'ws-note' }, [ E('input', { 'type': 'checkbox', 'checked': this.view.older ? '' : null,
			'change': function(ev) { self.view.older = ev.target.checked; self.order = null; self.drawList(); } }), ' ', _('show devices not seen this week') ]);
		this.root.replaceChildren(
			E('h2', {}, [ _('Devices') ]),
			E('p', { 'class': 'ws-sub' }, [ _('Every device that used this router. Open one to see its traffic, rename it, or pause, limit or schedule its internet.') ]),
			this.waitBox,
			E('div', { 'class': 'ws-card' }, [ E('div', { 'class': 'ws-bar' }, [ search, older, E('span', { 'class': 'grow' }), this.sumBox ]), this.listBox ])
		);
		this.drawList();
	},

	sortBy: function(key) {
		this.view.sort = this.view.sort == key ? key + '-asc' : key;
		this.order = null;
		this.drawList();
	},

	drawList: function() {
		if (!this.listBox) return;
		var self = this, v = this.view, now = Date.now() / 1000;
		var tot = function(c) { return c ? c.down + c.up : 0; };
		var byKey = {};
		this.devs.forEach(function(d) { byKey[d.key] = d; });
		var visible = function(d) {
			if (!v.older && !d.online && now - d.last_seen > 7 * 86400 && now - d.first_seen > 86400 && tot(d.today) == 0 && tot(d.month) == 0) return false;
			if (v.q) {
				var hay = (d.name + ' ' + (d.hostname || '') + ' ' + (d.mac || '') + ' ' + d.key + ' ' + d.ips.join(' ') + ' ' + (d.top || []).join(' ')).toLowerCase();
				if (hay.indexOf(v.q) < 0) return false;
			}
			return true;
		};
		if (!this.order) {
			var keyOf = { live: function(d) { return tot(d.rate); }, today: function(d) { return tot(d.today); }, month: function(d) { return tot(d.month); } };
			var s = v.sort.replace('-asc', ''), asc = /-asc$/.test(v.sort);
			this.order = this.devs.slice().sort(function(a, b) {
				var r = s == 'name' ? a.name.localeCompare(b.name) : keyOf[s](b) - keyOf[s](a) || a.name.localeCompare(b.name);
				return asc ? -r : r;
			}).map(function(d) { return d.key; });
		}
		// devices that appeared since the order was made go at the end
		this.devs.forEach(function(d) { if (self.order.indexOf(d.key) < 0) self.order.push(d.key); });
		var list = this.order.map(function(k) { return byKey[k]; }).filter(function(d) { return d && visible(d); });

		var online = this.devs.filter(function(d) { return d.online && d.key != 'router'; }).length;
		this.sumBox.replaceChildren(_('%d online, %d shown').format(online, list.length));
		var waiting = this.devs.filter(function(d) { return d.effect && d.effect.guest && !d.approved; });
		this.waitBox.replaceChildren(waiting.length ? E('div', { 'class': 'ws-warnbox' }, [
			_('Waiting for your approval (no internet or guest speed until then): '),
			E('span', {}, waiting.map(function(d, i) { return E('span', {}, [ i ? ', ' : '', E('a', { 'href': '#' + encodeURIComponent(d.key) }, [ d.name ]) ]); })) ]) : '');

		var th = function(label, key, cls) {
			var on = v.sort.replace('-asc', '') == key;
			return E('th', { 'class': (cls || '') + ' click', 'style': 'cursor:pointer;user-select:none', 'title': _('Sort'),
				'click': function() { self.sortBy(key); } }, [ label, on ? (/-asc$/.test(v.sort) ? ' ↑' : ' ↓') : '' ]);
		};
		var rows = list.map(function(d) {
			var wanTot = Object.keys(d.wans).reduce(function(s, w) { return s + tot(d.wans[w]); }, 0);
			var split = E('div', { 'class': 'ws-share', 'style': 'width:110px;height:6px;margin:0' }, Object.keys(d.wans).filter(function(w) { return tot(d.wans[w]) > 0; }).sort().map(function(w) {
				return E('span', { 'style': 'flex-grow:' + (tot(d.wans[w]) / (wanTot || 1)).toFixed(4) + ';background:' + (self.colors[w] || 'var(--ws-other)'),
					'title': self.label(w) + ': ' + ws.bytes(tot(d.wans[w])) });
			}));
			var r = d.rate || { down: 0, up: 0 }, ip4 = d.ips.filter(function(i) { return i.indexOf(':') < 0; })[0];
			return E('tr', { 'class': 'click', 'style': d.online ? '' : 'opacity:.55', 'click': function() { location.hash = encodeURIComponent(d.key); } }, [
				E('td', {}, [ E('div', { 'style': 'display:flex;gap:8px;align-items:center;min-width:230px' }, [
					E('span', { 'class': 'ws-dot', 'style': 'background:' + (d.online ? 'var(--ws-good)' : 'var(--ws-track)'),
						'title': d.online ? _('online') : d.last_seen ? _('last seen %s').format(ws.ago(d.last_seen)) : _('no traffic seen') }),
					E('span', { 'style': 'font-size:16px;width:20px;text-align:center', 'aria-hidden': 'true' }, [ ws.icon(d) ]),
					E('div', { 'style': 'min-width:0' }, [ E('div', { 'style': 'font-weight:600;display:flex;gap:6px;align-items:center;flex-wrap:wrap' }, [ d.name,
						E('button', { 'class': 'ws-btn', 'type': 'button', 'style': 'padding:0 6px;font-size:12px;line-height:18px', 'title': _('Rename or change icon'), 'aria-label': _('Rename or change icon'),
							'click': function(ev) { ev.stopPropagation(); ws.editName(d, function() { self.refresh(); }); } }, [ '✎' ]) ].concat(ws.chips(d))),
						E('small', {}, [ [ ip4, d.mac ].filter(Boolean).join(' · ') || (d.key == 'router' ? _('the router itself') : '') ]) ]) ]) ]),
				E('td', { 'class': 'r' }, tot(r) ? [ '↓ ' + ws.rate(r.down), E('br'), E('small', {}, [ '↑ ' + ws.rate(r.up) ]) ] : [ E('small', {}, [ '—' ]) ]),
				E('td', { 'class': 'r' }, [ ws.bytes(tot(d.today)) ]),
				E('td', { 'class': 'r' }, [ ws.bytes(tot(d.month)) ]),
				E('td', {}, [ split ]),
				E('td', { 'style': 'max-width:220px' }, [ E('small', {}, [ (d.top || []).join(', ') ]) ])
			]);
		});
		this.listBox.replaceChildren(list.length ? E('table', { 'class': 'ws-t' }, [
			E('tr', {}, [ th(_('Device'), 'name'), th(_('Now'), 'live', 'r'), th(_('Today'), 'today', 'r'), th(_('This month'), 'month', 'r'),
				E('th', {}, [ _('Uplinks today') ]), E('th', {}, [ _('Mostly') ]) ])
		].concat(rows)) : E('div', { 'class': 'ws-empty' }, [ _('No devices match.') ]));
	},
	// ---------- one device ----------
	// The page is built once; every second the tiles, actions, live chart and connections update, every
	// 5 seconds the history and the breakdowns. Parts with inputs (name, limits) are only redrawn when what
	// they show changed, and never while you type in them.
	renderDetail: function() {
		var self = this;
		this.box = {
			title: E('div', { 'style': 'display:flex;gap:12px;align-items:center;flex-wrap:wrap' }), tiles: E('div', { 'class': 'ws-tiles', 'style': 'margin:12px 0 4px' }),
			actions: E('div'), live: E('div'), liveNow: E('span', { 'class': 'ws-note' }), hist: E('div'), histBar: E('span'),
			split: E('div'), services: E('div'), sites: E('div'), flows: E('div'), rules: E('div'), info: E('div')
		};
		this.sig = {};
		this.ticks = 0;
		var b = this.box;
		this.root.replaceChildren(
			E('a', { 'class': 'ws-back', 'href': '#', 'click': function(ev) { ev.preventDefault(); history.pushState(null, '', location.pathname); self.route(); } }, [ '← ', _('All devices') ]),
			E('div', { 'class': 'ws-card' }, [ b.title, b.tiles, b.actions ]),
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Right now'), E('span', { 'class': 'grow' }), b.liveNow ]), b.live ]),
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Traffic by uplink'), E('span', { 'class': 'grow' }), b.histBar ]), b.hist, b.split ]),
			E('div', { 'class': 'ws-cols' }, [
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Services') ]), b.services ]),
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Sites and addresses') ]), b.sites ]) ]),
			E('div', { 'class': 'ws-cols' }, [
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Limits and rules') ]), b.rules ]),
				E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('About this device') ]), b.info ]) ]),
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Open connections') ]), b.flows ])
		);
		this.loadDetail();
		ws.live(0, 300, true).then(function(lv) { self.liveBuf = null; self.mergeLive(lv); self.drawDetailLive(); });
	},

	refreshDetail: function() {
		var self = this, key = this.cur;
		this.ticks++;
		return Promise.all([ ws.devices(), ws.live(this.liveTo || 0, 300, true), ws.flows(key, '', 300),
			this.ticks % 5 == 0 ? this.loadDetail() : null ]).then(function(r) {
			if (self.cur != key) return;
			self.devs = r[0].devices || self.devs;
			self.mergeLive(r[1]);
			self.drawDetailLive();
			var d = self.dev();
			self.drawTitle(d);
			self.drawTiles(d);
			self.drawActions(d);
			self.drawRules(d);
			self.drawFlows(r[2].flows || []);
		});
	},

	loadDetail: function() {
		var self = this, r = range(this.view.range), key = this.cur;
		var keys = [ 'dw:' + key + '|*' ];
		return Promise.all([ ws.device(key, r.from, r.to), ws.history(keys, r.from, r.to, r.step) ]).then(function(res) {
			if (self.cur != key) return;
			self.detail = res[0];
			self.drawDetail(res[1], r);
		}).catch(function(e) {
			if (self.detail) return; // a failed refresh keeps what is shown
			self.root.replaceChildren(E('a', { 'class': 'ws-back', 'href': '#' }, [ '← ', _('All devices') ]),
				E('div', { 'class': 'ws-empty' }, [ _('This device is not known (%s).').format(e.message) ]));
		});
	},

	dev: function() {
		var key = this.cur;
		return this.devs.filter(function(d) { return d.key == key; })[0] || (this.detail && this.detail.device) || { key: key, name: key, effect: {}, ips: [], wans: {}, today: { down: 0, up: 0 }, month: { down: 0, up: 0 } };
	},

	mergeLive: function(lv) {
		if (!lv || !lv.to) return;
		var p = ws.unpack((lv.devices || {})[this.cur]), n = lv.to - lv.from + 1;
		if (!p.down.length) { p.down = new Array(n).fill(0); p.up = new Array(n).fill(0); }
		if (!this.liveBuf || lv.from > this.liveTo + 1) this.liveBuf = { from: lv.from, down: [], up: [] };
		this.liveBuf.down = this.liveBuf.down.concat(p.down);
		this.liveBuf.up = this.liveBuf.up.concat(p.up);
		var cut = this.liveBuf.down.length - 300;
		if (cut > 0) { this.liveBuf.down.splice(0, cut); this.liveBuf.up.splice(0, cut); this.liveBuf.from += cut; }
		this.liveTo = lv.to;
	},

	drawDetailLive: function() {
		var b = this.box, buf = this.liveBuf;
		if (!b || !buf || !buf.down.length) return;
		var n = buf.down.length, c = ws.devColor(this.cur);
		var last = function(a) { var s = 0, k = 0; for (var i = Math.max(0, a.length - 3); i < a.length; i++) { s += a[i]; k++; } return k ? s / k : 0; };
		b.liveNow.replaceChildren('↓ ' + ws.rate(last(buf.down)) + '   ↑ ' + ws.rate(last(buf.up)));
		if (ws.hovering()) return;
		var W = ws.plotWidth(b.live);
		var mk = function(vals, title, h) {
			return ws.areas({ W: W, H: h, t0: buf.from, step: 1, n: n, minMax: 1000, title: title,
				layers: [ { label: title, color: c, vals: vals } ], fmt: function(v) { return ws.rate(v); } }).svg;
		};
		b.live.replaceChildren(E('div', { 'class': 'ws-plot-label' }, [ _('Download') ]), mk(buf.down, _('download'), 120),
			E('div', { 'class': 'ws-plot-label' }, [ _('Upload') ]), mk(buf.up, _('upload'), 80));
	},

	// a part is redrawn only when its data changed (and, with inputs, when none of them has focus)
	changed: function(part, data, box) {
		var s = JSON.stringify(data);
		if (this.sig[part] === s) return false;
		if (box && box.contains(document.activeElement) && /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName)) return false;
		this.sig[part] = s;
		return true;
	},

	drawTitle: function(d) {
		var self = this, b = this.box;
		if (!this.changed('title', [ d.name, d.effect, d.icon ])) return;
		var edit = function() { ws.editName(d, function() { self.refresh(); }); };
		b.title.replaceChildren.apply(b.title, [
			E('button', { 'class': 'ws-btn', 'type': 'button', 'style': 'font-size:26px;line-height:1.2;padding:2px 8px', 'title': _('Change the icon'), 'aria-label': _('Change the icon'), 'click': edit }, [ ws.icon(d) ]),
			E('div', { 'style': 'font-size:22px;font-weight:650' }, [ d.name ]),
			E('button', { 'class': 'ws-btn', 'type': 'button', 'click': edit }, [ '✎ ', _('Rename or change icon') ])
		].concat(ws.chips(d)));
	},
	drawTiles: function(d) {
		var tot = function(c) { return c ? c.down + c.up : 0; }, r = d.rate || { down: 0, up: 0 };
		this.box.tiles.replaceChildren(
			ws.tile(_('Now'), ws.rateParts(r.down)[0], ws.rateParts(r.down)[1], '↑ ' + ws.rate(r.up)),
			ws.tile(_('Today'), ws.bytesParts(tot(d.today))[0], ws.bytesParts(tot(d.today))[1], '↓ ' + ws.bytes(d.today.down) + '  ↑ ' + ws.bytes(d.today.up)),
			ws.tile(_('This month'), ws.bytesParts(tot(d.month))[0], ws.bytesParts(tot(d.month))[1], '↓ ' + ws.bytes(d.month.down) + '  ↑ ' + ws.bytes(d.month.up)),
			ws.tile(_('Connections'), String(d.flows || 0), '', d.online ? _('online now') : d.last_seen ? _('last seen %s').format(ws.ago(d.last_seen)) : _('no traffic seen')),
			ws.tile(_('Inside the LAN today'), ws.bytesParts(tot(d.local))[0], ws.bytesParts(tot(d.local))[1], _('to the router itself (DNS, settings)'))
		);
	},

	drawActions: function(d) {
		if (!this.changed('actions', [ d.effect, d.policy, d.approved, d.key ])) return;
		this.box.actions.replaceChildren(this.actionBar(d));
	},

	drawDetail: function(hist, r) {
		var self = this, b = this.box, det = this.detail, d = this.devs.filter(function(x) { return x.key == det.device.key; })[0] || det.device;
		var tot = function(c) { return c ? c.down + c.up : 0; };
		this.drawTitle(d);
		this.drawTiles(d);
		this.drawActions(d);

		// history by uplink (held still while the pointer is on it)
		b.histBar.replaceChildren(ws.seg(RANGES, this.view.range, function(v) { self.view.range = v; self.sig.hist = null; self.loadDetail(); }));
		var bins = (hist.bins || '').split(',').filter(Boolean).map(Number);
		var layers = Object.keys(hist.series || {}).sort().map(function(k) {
			var w = k.split('|')[1], p = ws.unpack(hist.series[k]);
			return { label: self.label(w), color: self.colors[w] || 'var(--ws-other)', vals: p.down.map(function(v, i) { return v + p.up[i]; }) };
		});
		if (!ws.hovering() && this.changed('hist', [ this.view.range, hist.bins, hist.series ]))
			b.hist.replaceChildren(bins.length && layers.length ? ws.bars({ W: ws.plotWidth(b.hist), H: 190, bins: bins, layers: layers,
				fmt: function(v) { return ws.bytes(v); }, fmtBin: r.fmt, fmtTip: r.tip }) : E('div', { 'class': 'ws-empty' }, [ _('No internet traffic in this range.') ]));
		var wl = Object.keys(det.wans).filter(function(w) { return w[0] != '(' && tot(det.wans[w]) > 0; });
		b.split.replaceChildren(E('div', { 'class': 'ws-legend' }, wl.map(function(w) {
			return E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:' + (self.colors[w] || 'var(--ws-other)') }), self.label(w),
				E('b', {}, [ ws.bytes(tot(det.wans[w])) ]), E('span', { 'class': 'ws-note' }, [ '↓ ' + ws.bytes(det.wans[w].down) + ' ↑ ' + ws.bytes(det.wans[w].up) ]) ]);
		})));

		var bd = det.breakdown || {};
		b.services.replaceChildren(ws.rows((bd.services || []).slice(0, 15).map(function(s) {
			return { label: s.name, sub: s.category != 'other' ? s.category : '', down: s.down, up: s.up, color: ws.catColor(s.category) };
		})));
		var sites = (bd.sites || []).concat(bd.addrs || []).sort(function(a, b) { return b.down + b.up - (a.down + a.up); }).slice(0, 15);
		b.sites.replaceChildren(ws.rows(sites.map(function(s) { return { label: s.name, down: s.down, up: s.up, color: 'var(--ws-c1)' }; })));
		if ((bd.procs || []).length)
			b.sites.appendChild(E('div', { 'style': 'margin-top:12px' }, [ E('div', { 'class': 'ws-plot-label' }, [ _('Programs on the router') ]),
				ws.rows(bd.procs.map(function(p) { return { label: p.name == '?' ? _('(kernel or unknown)') : p.name, down: p.down, up: p.up, color: 'var(--ws-c6)' }; })) ]));

		if (!this.sig.flowsShown) { this.sig.flowsShown = true; this.drawFlows(det.flows || []); }
		this.drawRules(d);
		b.info.replaceChildren(E('div', { 'class': 'ws-kv' }, [
			E('div', {}, [ _('Addresses') ]), E('div', {}, [ (d.ips || []).join(', ') || '—' ]),
			E('div', {}, [ _('MAC') ]), E('div', {}, [ d.mac || '—', d.private ? E('div', { 'class': 'ws-note' }, [ _('A private (random) Wi-Fi address: the phone may use another one on other networks or after a reset.') ]) : '' ]),
			E('div', {}, [ _('Name from DHCP') ]), E('div', {}, [ d.hostname || '—' ]),
			d.modem ? E('div', {}, [ _('Role') ]) : '', d.modem ? E('div', {}, [ _('the modem of uplink %s').format(self.label(d.modem)) ]) : '',
			E('div', {}, [ _('First seen') ]), E('div', {}, [ d.first_seen ? new Date(d.first_seen * 1000).toLocaleString() : '—' ]),
			E('div', {}, [ _('Last traffic') ]), E('div', {}, [ d.last_seen ? new Date(d.last_seen * 1000).toLocaleString() : '—' ]),
			E('div', {}, [ _('Dropped by limits') ]), E('div', {}, [ tot(d.drops) ? ws.bytes(tot(d.drops)) : '—' ])
		]), d.key != 'router' ? E('div', { 'style': 'margin-top:12px' }, [ E('button', { 'class': 'ws-btn', 'click': function() { self.forget(d); } }, [ _('Forget this device') ]) ]) : '');
	},
	actionBar: function(d) {
		var self = this, e = d.effect || {}, p = d.policy || {};
		if (d.key == 'router') {
			return d.key == 'router' ? E('div', { 'class': 'ws-note' }, [ _('The router\'s own traffic: DNS lookups for the LAN, VPN clients, updates and downloads it makes itself.') ]) : E('div');
		}
		var tomorrow = function() { var t = new Date(); t.setDate(t.getDate() + (t.getHours() >= 7 ? 1 : 0)); t.setHours(7, 0, 0, 0); return Math.ceil((t - Date.now()) / 60000); };
		var kids = [];
		if (e.guest && !d.approved)
			kids.push(E('button', { 'class': 'ws-btn on', 'click': function() { self.save({ approved: true }, _('Approved: it has internet now.')); } }, [ _('Approve') ]));
		if (p.pause_until > Date.now() / 1000)
			kids.push(E('button', { 'class': 'ws-btn on', 'click': function() { self.save({ pause: 0 }, _('Internet is back on.')); } }, [ _('Resume internet') ]),
				E('span', { 'class': 'ws-note' }, [ p.pause_until > 4e9 ? _('paused until you resume') : _('paused until %s').format(new Date(p.pause_until * 1000).toLocaleString()) ]));
		else
			kids.push(E('span', { 'class': 'ws-note' }, [ _('Pause internet:') ]), ws.seg([ [ 15, _('15 min') ], [ 60, _('1 h') ], [ 120, _('2 h') ], [ 'm', _('until 7:00') ], [ -1, _('until I resume') ] ], '', function(v) {
				self.save({ pause: v == 'm' ? tomorrow() : +v }, _('Paused.'));
			}));
		kids.push(E('span', { 'class': 'grow' }));
		kids.push(p.block
			? E('button', { 'class': 'ws-btn on', 'click': function() { self.save({ block: false }, _('Unblocked.')); } }, [ _('Unblock') ])
			: E('button', { 'class': 'ws-btn bad', 'click': function() { self.save({ block: true }, _('Blocked: no internet until you unblock it.')); } }, [ _('Block') ]));
		var why = (e.reasons || []).length ? E('div', { 'class': 'ws-note', 'style': 'margin-top:6px' }, [ _('Now: %s').format(e.reasons.join('; ')) ]) : '';
		return E('div', {}, [ E('div', { 'class': 'ws-bar', 'style': 'margin:8px 0 0' }, kids), why ]);
	},

	drawRules: function(d) {
		var self = this, b = this.box, p = d.policy || { schedules: [], quotas: [] };
		if (!this.changed('rules', [ d.key, d.policy ], b.rules)) return;
		if (d.key == 'router') { b.rules.replaceChildren(E('div', { 'class': 'ws-note' }, [ _('The router itself has no limits.') ])); return; }
		var down = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0, 'step': 0.1, 'value': toMB(p.down), 'placeholder': _('no limit'), 'style': 'width:110px' });
		var up = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0, 'step': 0.1, 'value': toMB(p.up), 'placeholder': _('no limit'), 'style': 'width:110px' });
		var scheds = (p.schedules || []).slice(), quotas = (p.quotas || []).slice();
		var list = function(items, describe, onDel) {
			return items.length ? E('div', { 'class': 'ws-rows' }, items.map(function(s, i) {
				return E('div', { 'class': 'ws-row' }, [ E('div', { 'class': 'n' }, [ E('span', { 'class': 't' }, [ describe(s) ]) ]),
					E('div', { 'class': 'v' }, [ E('button', { 'class': 'ws-btn', 'click': function() { onDel(i); } }, [ _('Remove') ]) ]) ]);
			})) : E('div', { 'class': 'ws-note' }, [ _('None.') ]);
		};
		b.rules.replaceChildren(
			E('div', { 'class': 'ws-plot-label' }, [ _('Speed limit in MB/s (empty for none)') ]),
			E('div', { 'class': 'ws-bar' }, [ _('Download'), down, _('Upload'), up,
				E('button', { 'class': 'ws-btn', 'click': function() { self.save({ down: toKB(down.value), up: toKB(up.value) }, _('Speed limit saved.')); } }, [ _('Save') ]) ]),
			E('div', { 'class': 'ws-plot-label' }, [ _('Schedules'), E('button', { 'class': 'ws-btn', 'click': function() { self.editSchedule(scheds); } }, [ _('Add') ]) ]),
			list(scheds, describeSchedule, function(i) { scheds.splice(i, 1); self.save({ schedules: scheds }, _('Schedule removed.')); }),
			E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:14px' }, [ _('Data quotas'), E('button', { 'class': 'ws-btn', 'click': function() { self.editQuota(quotas); } }, [ _('Add') ]) ]),
			list(quotas, describeQuota, function(i) { quotas.splice(i, 1); self.save({ quotas: quotas }, _('Quota removed.')); }),
			E('div', { 'class': 'ws-note', 'style': 'margin-top:10px' }, [ _('Limits drop traffic above the rate on this router; the device\'s connections slow down to fit.') ])
		);
	},

	editSchedule: function(scheds) {
		var self = this;
		var boxes = DAYS.map(function(d) { return E('label', { 'style': 'margin-right:8px' }, [ E('input', { 'type': 'checkbox', 'value': d[0], 'checked': true }), ' ', d[1] ]); });
		var from = E('input', { 'class': 'ws-in', 'type': 'time', 'value': '23:00' }), to = E('input', { 'class': 'ws-in', 'type': 'time', 'value': '07:00' });
		var act = E('select', { 'class': 'ws-in' }, [ E('option', { 'value': 'block' }, [ _('No internet') ]), E('option', { 'value': 'limit' }, [ _('Limit speed') ]) ]);
		var down = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0.01, 'step': 0.1, 'value': 1, 'style': 'width:90px' }), up = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0, 'step': 0.1, 'value': 0.5, 'style': 'width:90px' });
		ui.showModal(_('Add a schedule'), [
			E('p', {}, boxes), E('p', {}, [ _('From'), ' ', from, ' ', _('to'), ' ', to, ' ', E('span', { 'class': 'ws-note' }, [ _('(past midnight is fine)') ]) ]),
			E('p', {}, [ act, ' ', _('download'), ' ', down, ' ', _('upload'), ' ', up, ' MB/s' ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					var days = boxes.map(function(b) { return b.firstChild; }).filter(function(c) { return c.checked; }).map(function(c) { return c.value; });
					if (!days.length) return;
					var s = (days.length == 7 ? 'daily' : days.join(',')) + ' ' + from.value + '-' + to.value + ' ' + act.value + (act.value == 'limit' ? ' ' + Math.max(1, toKB(down.value)) + ' ' + toKB(up.value) : '');
					ui.hideModal();
					self.save({ schedules: scheds.concat([ s ]) }, _('Schedule added.'));
				} }, [ _('Add') ]) ]) ]);
	},

	editQuota: function(quotas) {
		var self = this;
		var per = E('select', { 'class': 'ws-in' }, [ [ 'day', _('a day') ], [ 'week', _('a week') ], [ 'month', _('a month') ] ].map(function(o) { return E('option', { 'value': o[0] }, [ o[1] ]); }));
		var dir = E('select', { 'class': 'ws-in' }, [ [ 'total', _('in all') ], [ 'down', _('download') ], [ 'up', _('upload') ] ].map(function(o) { return E('option', { 'value': o[0] }, [ o[1] ]); }));
		var gb = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0.01, 'step': 0.1, 'value': 2, 'style': 'width:90px' });
		var act = E('select', { 'class': 'ws-in' }, [ [ 'alert', _('just alert me') ], [ 'limit', _('limit its speed') ], [ 'block', _('turn its internet off') ] ].map(function(o) { return E('option', { 'value': o[0] }, [ o[1] ]); }));
		var down = E('input', { 'class': 'ws-in', 'type': 'number', 'min': 0.01, 'step': 0.1, 'value': 0.5, 'style': 'width:90px' });
		ui.showModal(_('Add a data quota'), [
			E('p', {}, [ gb, ' GB ', dir, ' ', per, ', ', _('then'), ' ', act ]),
			E('p', {}, [ _('Speed after the quota, for "limit its speed" (MB/s)'), ' ', down ]),
			E('p', { 'class': 'ws-note' }, [ _('Days and weeks start at midnight (weeks on Monday), months on the 1st. Only internet traffic counts.') ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					var mb = Math.round((+gb.value || 0) * 1000);
					if (mb <= 0) return;
					var q = per.value + ' ' + dir.value + ' ' + mb + ' ' + act.value + (act.value == 'limit' ? ' ' + Math.max(1, toKB(down.value)) + ' ' + Math.max(1, toKB(down.value)) : '');
					ui.hideModal();
					self.save({ quotas: quotas.concat([ q ]) }, _('Quota added.'));
				} }, [ _('Add') ]) ]) ]);
	},

	save: function(o, msg) {
		var self = this;
		return ws.setDevice(this.cur, o).then(function() {
			ui.addNotification(null, E('p', {}, [ msg ]), 'info');
			return ws.devices().then(function(r) { self.devs = r.devices || self.devs; return self.loadDetail(); });
		}).catch(function(e) { ui.addNotification(null, E('p', {}, [ _('Not saved: %s').format(e.message) ]), 'error'); });
	},

	forget: function(d) {
		var self = this;
		ui.showModal(_('Forget %s?').format(d.name), [
			E('p', {}, [ _('Its name and rules are removed. Its traffic history stays. If it connects again it shows up as a new device.') ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-negative', 'click': function() {
					ui.hideModal();
					ws.forget(d.key).then(function() { location.hash = ''; });
				} }, [ _('Forget') ]) ]) ]);
	},

	drawFlows: function(flows) {
		var self = this;
		flows = flows.filter(function(f) { return !f.lan; }).slice(0, 60);
		this.box.flows.replaceChildren(flows.length ? E('div', { 'class': 'ws-scroll' }, [ E('table', { 'class': 'ws-t' }, [
			E('tr', {}, [ E('th', {}, [ _('Server') ]), E('th', {}, [ _('Service') ]), E('th', {}, [ _('Uplink') ]), E('th', { 'class': 'r' }, [ _('Now') ]),
				E('th', { 'class': 'r' }, [ _('In all') ]), E('th', { 'class': 'r' }, [ _('Open for') ]) ])
		].concat(flows.map(function(f) {
			return E('tr', {}, [
				E('td', {}, [ f.host || f.remote, E('br'), E('small', {}, [ f.proto + ' ' + f.remote + (f.src ? ' · ' + _('named by %s').format(f.src) : '') ]) ]),
				E('td', {}, [ f.service || '' ]),
				E('td', {}, [ f.wan ? E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:' + (self.colors[f.wan] || 'var(--ws-other)') + ';margin-right:6px' }), self.label(f.wan) ]) : '—' ]),
				E('td', { 'class': 'r' }, [ f.rate_down + f.rate_up ? '↓ ' + ws.rate(f.rate_down) : '', f.rate_down + f.rate_up ? E('br') : '', f.rate_down + f.rate_up ? E('small', {}, [ '↑ ' + ws.rate(f.rate_up) ]) : '' ]),
				E('td', { 'class': 'r' }, [ ws.bytes(f.down + f.up) ]),
				E('td', { 'class': 'r' }, [ ws.dur(Date.now() / 1000 - f.first) ])
			]);
		})))]) : E('div', { 'class': 'ws-empty' }, [ _('No open connections.') ]));
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
