'use strict';
'require view';
'require poll';
'require ui';
'require wanscope.ui as ws';

// Network > wanscope > Activity: what every device is doing right now, by service, down to each connection.
// Refreshes every second. Devices and services keep their places between refreshes (new ones join at the end,
// quiet ones leave); "Busiest first" sorts them again. A service opens to its connections with a click.

var ACTIVE = 5; // seconds: traffic this recent is "now" (as on the overview)
var KEEP = 30;  // seconds a quiet connection stays listed, so rows don't come and go under the pointer

// names a port gives that already say what the traffic is: nothing to name
var PROTOCOLS = { 'DNS': true, 'NTP': true, 'Ping': true, 'Mail': true, 'SSH': true, 'WireGuard': true, 'Calls (STUN)': true };

var KINDS = function() {
	return [ [ 'video', _('video') ], [ 'games', _('games') ], [ 'social', _('social') ], [ 'messaging', _('messaging') ], [ 'music', _('music') ],
		[ 'cloud', _('cloud') ], [ 'updates', _('updates') ], [ 'work', _('work') ], [ 'other', _('other') ] ];
};

var SOURCES = function() {
	return { sni: _('TLS server name'), quic: _('QUIC server name'), dns: _('its DNS lookup'), 'dns*': _('another device\'s DNS lookup') };
};

// what a connection talks to: its service, else its site, its server name or its address
function labelOf(f) { return f.service || f.site || f.host || addrOf(f.remote); }
function addrOf(ap) { var m = /^\[(.*)\]:\d+$/.exec(ap) || /^(.*):\d+$/.exec(ap); return m ? m[1] : ap; }
function portOf(ap) { var m = /:(\d+)$/.exec(ap); return m ? m[1] : ''; }
function blockedHere(d) { return (d && d.policy && d.policy.block_services) || []; }
function protoOf(f) { return f.proto == 'udp' && portOf(f.remote) == '443' ? 'QUIC' : f.proto.toUpperCase(); }

return view.extend({
	load: function() {
		return Promise.all([ ws.status(), ws.flows('', '', 2000), ws.devices(), ws.live(0, 60, true) ]);
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.st = res[0] || {};
		this.colors = ws.linkColors(this.st.ifaces);
		this.take(res[1]);
		this.devs = (res[2] || {}).devices || [];
		this.live = res[3] || {};
		this.v = { q: '', wan: '', idle: false, lan: false };
		this.order = null;     // device keys, in the order shown
		this.svcOrder = {};    // device key -> service labels, in the order shown
		this.open = {};        // "device\0service" -> its connections are shown

		this.tiles = E('div', { 'class': 'ws-tiles' });
		this.list = E('div');
		// the list holds still under a mouse (rows would move under the pointer); it catches up when it leaves
		this.list.addEventListener('pointerenter', function(ev) { if (ev.pointerType == 'mouse') self.held = true; });
		this.list.addEventListener('pointerleave', function(ev) { if (ev.pointerType == 'mouse') { self.held = false; self.draw(); } });
		this.countBox = E('span', { 'class': 'ws-note' });
		this.blockedBox = E('div');
		this.wanSel = E('select', { 'class': 'ws-in', 'change': function(ev) { self.v.wan = ev.target.value; self.draw(); } });
		var check = function(key, label) {
			return E('label', { 'class': 'ws-note' }, [ E('input', { 'type': 'checkbox',
				'change': function(ev) { self.v[key] = ev.target.checked; self.draw(); } }), ' ', label ]);
		};
		var root = E('div', { 'class': 'ws' }, [
			E('h2', {}, [ _('Activity') ]),
			E('p', { 'class': 'ws-sub' }, [ _('What every device is doing right now: each service it uses, how fast, over which uplink, and every connection behind it. Click a service to see its connections.') ]),
			E('div', { 'class': 'ws-card' }, [
				this.tiles,
				E('div', { 'class': 'ws-bar', 'style': 'margin-top:12px' }, [
					E('input', { 'class': 'ws-in', 'type': 'search', 'placeholder': _('Find a device, service, server, address or program'), 'style': 'min-width:280px',
						'input': function(ev) { self.v.q = ev.target.value.toLowerCase(); self.draw(); } }),
					this.wanSel,
					check('idle', _('include quiet connections')),
					check('lan', _('include LAN-only')),
					E('button', { 'class': 'ws-btn', 'type': 'button', 'click': function() { self.order = null; self.svcOrder = {}; self.draw(); } }, [ _('Busiest first') ]),
					E('span', { 'class': 'grow' }), this.countBox ]),
				this.blockedBox ]),
			this.list,
			this.credits()
		]);
		this.draw();

		var tick = 0;
		poll.add(function() {
			tick++;
			return Promise.all([ ws.flows('', '', 2000), ws.live(0, 60, true), tick % 5 == 0 ? ws.devices() : null ]).then(function(r) {
				self.take(r[0]);
				self.live = r[1] || self.live;
				if (r[2]) self.devs = r[2].devices || self.devs;
				self.draw();
			});
		}, 1);
		return root;
	},

	// the data package's sources, credited where their names show (DB-IP's licence asks for it)
	credits: function() {
		var d = this.st.data || {}, parts = [];
		if (d.services) parts.push(E('span', {}, [ _('More service names from '), E('a', { 'href': 'https://github.com/v2fly/domain-list-community', 'target': '_blank', 'rel': 'noreferrer' }, [ 'v2fly/domain-list-community' ]), '. ' ]));
		if (d.owners) parts.push(E('span', {}, [ _('Address owners: '), E('a', { 'href': 'https://db-ip.com', 'target': '_blank', 'rel': 'noreferrer' }, [ _('IP Geolocation by DB-IP') ]),
			' (CC BY 4.0)', d.owners_built ? _(', from %s').format(ws.dayLabel(d.owners_built, true)) : '', '.' ]));
		return parts.length ? E('p', { 'class': 'ws-note', 'style': 'margin-top:4px' }, parts) : E('span');
	},

	take: function(r) {
		r = r || {};
		this.flows = r.flows || [];
		this.total = r.total || 0;
		this.now = r.now || Date.now() / 1000; // the router's clock: "last traffic" is compared with it
	},

	label: function(name) {
		var i = (this.st.ifaces || []).filter(function(x) { return x.name == name; })[0];
		return i ? i.label : name;
	},

	wanKey: function(w, rate) {
		return E('span', { 'style': 'display:inline-flex;align-items:center;gap:5px;margin-right:10px;white-space:nowrap' }, [
			E('span', { 'class': 'ws-key', 'style': 'background:' + (this.colors[w] || 'var(--ws-other)') }),
			this.label(w), rate != null ? E('small', {}, [ ws.rate(rate) ]) : '' ]);
	},

	// the visible connections grouped: device -> service -> connections
	group: function() {
		var self = this, v = this.v, now = this.now, byDev = {};
		this.flows.forEach(function(f) {
			if (!f.dev || (f.lan && !v.lan)) return;
			if (!v.idle && now - f.last >= KEEP) return;
			if (v.wan && f.wan != v.wan) return;
			if (v.q && [ f.dev_name, f.service, f.site, f.host, f.alias, f.remote, f.proc, f.tunnel ].join(' ').toLowerCase().indexOf(v.q) < 0) return;
			var d = byDev[f.dev] || (byDev[f.dev] = { key: f.dev, name: f.dev_name || f.dev, down: 0, up: 0, conns: 0, active: 0, wans: {}, svcs: {} });
			var name = labelOf(f), s = d.svcs[name] || (d.svcs[name] = { name: name, category: '', down: 0, up: 0, bytes: 0, conns: 0, active: 0,
				first: f.first, last: 0, servers: {}, wans: {}, protos: {}, flows: [], rule: false });
			s.rule = s.rule || !!f.rule || f.category == 'tunnel' || f.category == 'router' || !!PROTOCOLS[f.service];
			// the router's programs, tunnels, protocols and "Other" aren't services to block
			s.special = s.special || f.category == 'tunnel' || f.category == 'router' || !!PROTOCOLS[f.service] || f.service == 'Other';
			var busy = now - f.last < ACTIVE;
			d.down += f.rate_down; d.up += f.rate_up; d.conns++; if (busy) d.active++;
			if (f.wan) d.wans[f.wan] = (d.wans[f.wan] || 0) + f.rate_down + f.rate_up;
			s.category = s.category || f.category || '';
			s.down += f.rate_down; s.up += f.rate_up; s.bytes += f.down + f.up; s.conns++; if (busy) s.active++;
			s.first = Math.min(s.first, f.first); s.last = Math.max(s.last, f.last);
			var server = f.host || addrOf(f.remote);
			s.servers[server] = (s.servers[server] || 0) + f.down + f.up;
			if (f.wan) s.wans[f.wan] = true; else if (f.lan) s.wans.lan = true;
			s.protos[protoOf(f)] = true;
			s.flows.push(f);
		});
		return byDev;
	},

	draw: function(force) {
		var self = this, now = this.now, byDev = this.group();
		var devInfo = {};
		this.devs.forEach(function(d) { devInfo[d.key] = d; });

		// uplink choices (kept while the select has focus)
		if (document.activeElement != this.wanSel) {
			var wans = {};
			this.flows.forEach(function(f) { if (f.wan) wans[f.wan] = true; });
			this.wanSel.replaceChildren.apply(this.wanSel, [ E('option', { 'value': '' }, [ _('All uplinks') ]) ].concat(Object.keys(wans).sort().map(function(w) {
				return E('option', { 'value': w, 'selected': w == self.v.wan ? '' : null }, [ self.label(w) ]);
			})));
		}

		// stable order: devices and services keep their places, newcomers join at the end
		var rate = function(x) { return x.down + x.up; };
		var keys = Object.keys(byDev);
		if (!this.order)
			this.order = keys.sort(function(a, b) { return rate(byDev[b]) - rate(byDev[a]) || byDev[b].conns - byDev[a].conns; });
		else {
			keys.forEach(function(k) { if (self.order.indexOf(k) < 0) self.order.push(k); });
		}
		var shown = this.order.filter(function(k) { return byDev[k]; });

		// the tiles: everything shown, together
		var down = 0, up = 0, conns = 0, active = 0, svcs = 0;
		shown.forEach(function(k) { var d = byDev[k]; down += d.down; up += d.up; conns += d.conns; active += d.active; svcs += Object.keys(d.svcs).length; });
		var rd = ws.rateParts(down), ru = ws.rateParts(up);
		this.tiles.replaceChildren(
			ws.tile(_('Download now'), rd[0], rd[1], _('to the devices shown')),
			ws.tile(_('Upload now'), ru[0], ru[1], _('from the devices shown')),
			ws.tile(_('Devices active'), String(shown.filter(function(k) { return byDev[k].active > 0; }).length), '', _('with traffic in the last %d seconds').format(ACTIVE)),
			ws.tile(_('Connections'), String(active), '', _('moving data, of %d shown (%d open in all)').format(conns, this.total)),
			ws.tile(_('Services in use'), String(svcs), '', _('across those devices'))
		);
		this.countBox.replaceChildren(this.held && !force ? _('paused while the pointer is on the list') : _('refreshes every second'));
		var everywhere = this.st.block_services || [];
		this.blockedBox.replaceChildren(everywhere.length ? E('div', { 'class': 'ws-note', 'style': 'margin-top:10px' },
			[ _('Blocked on every device:'), ' ' ].concat(everywhere.map(function(n) { return self.blockedChip(n, ''); }),
				[ E('a', { 'href': L.url('admin/network/wanscope/blocked') }, [ _('All blocked services') ]) ])) : '');

		if (this.held && !force) return;
		if (!shown.length) {
			this.list.replaceChildren(E('div', { 'class': 'ws-card' }, [ E('div', { 'class': 'ws-empty' }, [
				this.v.q || this.v.wan ? _('Nothing matches.') : _('Nothing is moving right now. Tick "include quiet connections" to see every open connection.') ]) ]));
			return;
		}
		this.list.replaceChildren.apply(this.list, shown.map(function(k) { return self.drawDevice(byDev[k], devInfo[k] || { key: k, name: byDev[k].name, ips: [] }); }));
	},

	drawDevice: function(g, d) {
		var self = this, now = this.now;
		var ips = (d.ips || []).filter(function(i) { return i.indexOf(':') < 0; }).concat((d.ips || []).filter(function(i) { return i.indexOf(':') >= 0; }).slice(0, 1));
		var series = (this.live.devices || {})[g.key], vals = [];
		if (series) { var u = ws.unpack(series); vals = u.down.map(function(x, i) { return x + (u.up[i] || 0); }); }

		// services, in a stable order
		var names = Object.keys(g.svcs), order = this.svcOrder[g.key];
		if (!order) order = this.svcOrder[g.key] = names.slice().sort(function(a, b) {
			var x = g.svcs[a], y = g.svcs[b];
			return (y.down + y.up) - (x.down + x.up) || y.bytes - x.bytes || a.localeCompare(b);
		});
		else names.forEach(function(n) { if (order.indexOf(n) < 0) order.push(n); });

		var rows = [];
		order.filter(function(n) { return g.svcs[n]; }).forEach(function(n) {
			var s = g.svcs[n], id = g.key + '\0' + n, open = !!self.open[id], busy = s.down + s.up > 0;
			var servers = Object.keys(s.servers).sort(function(a, b) { return s.servers[b] - s.servers[a]; });
			rows.push(E('tr', { 'class': 'click', 'title': open ? _('Hide its connections') : _('Show its connections'),
				'click': function() { if (self.open[id]) delete self.open[id]; else self.open[id] = true; self.draw(true); } }, [
				E('td', {}, [ E('span', { 'style': 'display:inline-block;width:12px;opacity:.6', 'aria-hidden': 'true' }, [ open ? '▾' : '▸' ]),
					E('span', { 'class': 'ws-key', 'style': 'margin-right:6px;background:' + ws.catColor(s.category) }), E('b', {}, [ n ]),

					s.category && s.category != 'other' ? E('div', { 'style': 'margin-left:30px' }, [ E('small', {}, [ s.category ]) ]) : '' ]),
				E('td', { 'class': 'r' }, busy ? [ '↓ ' + ws.rate(s.down), E('br'), E('small', {}, [ '↑ ' + ws.rate(s.up) ]) ]
					: [ E('small', {}, [ now - s.last < 1 ? _('between bursts') : _('quiet %s').format(ws.dur(now - s.last)) ]) ]),
				E('td', { 'class': 'r' }, [ String(s.conns), s.active != s.conns ? E('div', {}, [ E('small', {}, [ _('%d moving').format(s.active) ]) ]) : '' ]),
				E('td', { 'style': 'max-width:280px' }, [ E('small', {}, [ servers.slice(0, 3).join(', ') + (servers.length > 3 ? ' ' + _('+%d more').format(servers.length - 3) : '') ]) ]),
				E('td', {}, Object.keys(s.wans).sort().map(function(w) { return w == 'lan' ? E('small', {}, [ _('LAN') ]) : self.wanKey(w); })),
				E('td', {}, [ E('small', {}, [ Object.keys(s.protos).sort().join(', ') ]) ]),
				E('td', { 'class': 'r' }, [ ws.dur(now - s.first) ]),
				E('td', { 'class': 'r' }, [ ws.bytes(s.bytes) ]),
				E('td', { 'class': 'r', 'style': 'white-space:nowrap' }, [ self.blockCell(n, s, g, d) ])
			]));
			if (open) rows.push(E('tr', {}, [ E('td', { 'colspan': 9, 'style': 'padding:0 0 10px 30px' }, [ s.rule ? '' : self.renameBar(s), self.drawFlows(s.flows) ]) ]));
		});

		var wanKeys = Object.keys(g.wans).filter(function(w) { return g.wans[w] > 0; }).sort(function(a, b) { return g.wans[b] - g.wans[a]; });
		return E('div', { 'class': 'ws-card' }, [
			E('div', { 'style': 'display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:8px' }, [
				E('span', { 'class': 'ws-dot', 'style': 'background:' + (d.online === false ? 'var(--ws-track)' : 'var(--ws-good)') }),
				E('span', { 'style': 'font-size:20px', 'aria-hidden': 'true' }, [ ws.icon(d) ]),
				E('div', { 'style': 'min-width:0' }, [
					E('div', { 'style': 'display:flex;gap:6px;align-items:center;flex-wrap:wrap' }, [
						E('a', { 'href': L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(g.key), 'style': 'font-weight:600;font-size:15px' }, [ d.name || g.name ]) ].concat(ws.chips(d))),
					E('small', {}, [ ips.concat(d.mac ? [ d.mac ] : []).join(' · ') || (g.key == 'router' ? _('the router itself') : g.key) ]) ]),
				E('span', { 'class': 'grow', 'style': 'flex:1' }),
				vals.length ? E('span', { 'title': _('the last minute') }, [ ws.spark(vals, ws.devColor(g.key), 120, 26) ]) : '',
				E('div', { 'style': 'text-align:right' }, [ E('b', { 'class': 'num' }, [ '↓ ' + ws.rate(g.down) ]), '  ', E('span', { 'class': 'num' }, [ '↑ ' + ws.rate(g.up) ]),
					E('div', {}, [ E('small', {}, [ _('%d connections · %d services').format(g.conns, Object.keys(g.svcs).length) ]) ]) ])
			]),
			wanKeys.length ? E('div', { 'class': 'ws-note', 'style': 'margin-bottom:6px' }, [ _('Uplinks now:'), ' ' ].concat(wanKeys.map(function(w) { return self.wanKey(w, g.wans[w]); }))) : '',
			blockedHere(d).length ? E('div', { 'class': 'ws-note', 'style': 'margin-bottom:6px' }, [ _('Blocked on this device:'), ' ' ].concat(blockedHere(d).map(function(n) { return self.blockedChip(n, g.key); }))) : '',
			E('div', { 'class': 'ws-scroll' }, [ E('table', { 'class': 'ws-t' }, [
				E('tr', {}, [ E('th', {}, [ _('Service') ]), E('th', { 'class': 'r' }, [ _('Now') ]), E('th', { 'class': 'r' }, [ _('Connections') ]),
					E('th', {}, [ _('Servers') ]), E('th', {}, [ _('Uplink') ]), E('th', {}, [ _('Protocol') ]),
					E('th', { 'class': 'r' }, [ _('Open for') ]), E('th', { 'class': 'r' }, [ _('In all') ]), E('th', {}, [ '' ]) ])
			].concat(rows)) ])
		]);
	},

	// one service's connections, every detail
	drawFlows: function(flows) {
		var self = this, now = this.now, src = SOURCES();
		flows = flows.slice().sort(function(a, b) { return (b.rate_down + b.rate_up) - (a.rate_down + a.rate_up) || (b.down + b.up) - (a.down + a.up); });
		return E('table', { 'class': 'ws-t', 'style': 'font-size:12px' }, [
			E('tr', {}, [ E('th', {}, [ _('Server') ]), E('th', {}, [ _('Address') ]), E('th', {}, [ _('Named by') ]), E('th', {}, [ _('Uplink') ]),
				E('th', {}, [ _('Program / tunnel') ]), E('th', { 'class': 'r' }, [ _('Now') ]), E('th', { 'class': 'r' }, [ _('In all') ]),
				E('th', { 'class': 'r' }, [ _('Opened') ]), E('th', { 'class': 'r' }, [ _('Last traffic') ]) ])
		].concat(flows.map(function(f) {
			var busy = f.rate_down + f.rate_up > 0;
			return E('tr', {}, [
				E('td', { 'style': 'max-width:260px;overflow:hidden;text-overflow:ellipsis' }, [ f.host || _('(no name)'),
					f.alias ? E('div', {}, [ E('small', { 'title': _('the CNAME it resolved through') }, [ _('via %s').format(f.alias) ]) ]) : '',
					f.org ? E('div', {}, [ E('small', { 'title': _('who announces this address') }, [ _('network: %s').format(f.org) ]) ]) : '' ]),
				E('td', {}, [ protoOf(f) + ' ' + f.remote, E('div', {}, [ E('small', {}, [ _('from port %s').format(portOf(f.local)) ]) ]) ]),
				E('td', {}, [ E('small', {}, [ f.src ? (src[f.src] || f.src) : _('not named') ]) ]),
				E('td', {}, [ f.wan ? self.wanKey(f.wan) : f.lan ? E('small', {}, [ _('LAN') ]) : '—' ]),
				E('td', {}, [ E('small', {}, [ [ f.proc, f.tunnel ? _('carries %s').format(f.tunnel) : '' ].filter(Boolean).join(' · ') || '—' ]) ]),
				E('td', { 'class': 'r' }, busy ? [ '↓ ' + ws.rate(f.rate_down), E('br'), E('small', {}, [ '↑ ' + ws.rate(f.rate_up) ]) ] : [ E('small', {}, [ '—' ]) ]),
				E('td', { 'class': 'r' }, [ ws.bytes(f.down + f.up), E('br'), E('small', {}, [ '↓ ' + ws.bytes(f.down) + ' ↑ ' + ws.bytes(f.up) ]) ]),
				E('td', { 'class': 'r' }, [ _('%s ago').format(ws.dur(now - f.first)) ]),
				E('td', { 'class': 'r' }, [ now - f.last < 1 ? _('now') : _('%s ago').format(ws.dur(now - f.last)) ])
			]);
		})));
	},

	// a service row's last cell: Block, or blocked (here or everywhere) with Unblock
	blockCell: function(n, s, g, d) {
		var self = this, here = blockedHere(d).indexOf(n) >= 0, all = (this.st.block_services || []).indexOf(n) >= 0;
		var small = 'padding:0 7px;font-size:12px;line-height:18px';
		if (here || all)
			return E('span', {}, [ E('span', { 'class': 'ws-chip bad', 'title': all ? _('blocked on every device') : _('blocked on this device') }, [ _('blocked') ]), ' ',
				E('button', { 'class': 'ws-btn', 'type': 'button', 'style': small, 'title': all ? _('Unblock on every device') : _('Unblock on this device'),
					'click': function(ev) { ev.stopPropagation(); self.unblock(n, all ? '' : g.key); } }, [ _('Unblock') ]) ]);
		if (s.special || g.key == 'router' || !Object.keys(s.wans).some(function(w) { return w != 'lan'; })) return '';
		return E('button', { 'class': 'ws-btn', 'type': 'button', 'style': small, 'title': _('Block %s').format(n),
			'click': function(ev) { ev.stopPropagation(); self.blockIt(n, g, d); } }, [ _('Block') ]);
	},

	blockedChip: function(n, dev) {
		var self = this;
		return E('span', { 'style': 'display:inline-flex;align-items:center;gap:3px;margin-right:8px' }, [ E('span', { 'class': 'ws-chip bad' }, [ n ]),
			E('button', { 'class': 'ws-btn', 'type': 'button', 'style': 'padding:0 5px;font-size:11px;line-height:16px', 'title': _('Unblock %s').format(n),
				'aria-label': _('Unblock %s').format(n), 'click': function() { self.unblock(n, dev); } }, [ '✕' ]) ]);
	},

	// after a block changes: the lists of what is blocked (status, devices) again
	refreshBlocks: function() {
		var self = this;
		return Promise.all([ ws.status(), ws.devices() ]).then(function(r) {
			self.st = r[0] || self.st;
			self.devs = (r[1] || {}).devices || self.devs;
			self.draw(true);
		});
	},

	unblock: function(n, dev) {
		var self = this;
		ws.blockService(n, dev, false).then(function() { return self.refreshBlocks(); })
			.catch(function(err) { ui.addNotification(null, E('p', {}, [ err.message ]), 'danger'); });
	},

	// Block: asks whether on this device or on every device; its addresses are blocked (nothing in DNS)
	blockIt: function(n, g, d) {
		var self = this, name = d.name || g.name;
		var go = function(dev) {
			ws.blockService(n, dev, true).then(function() {
				ui.hideModal();
				ui.addNotification(null, E('p', {}, [ dev ? _('%s is blocked on %s.').format(n, name) : _('%s is blocked on every device.').format(n) ]), 'info');
				return self.refreshBlocks();
			}).catch(function(err) { ui.addNotification(null, E('p', {}, [ err.message ]), 'danger'); });
		};
		ui.showModal(_('Block %s').format(n), [
			E('p', {}, [ _('Its servers\' addresses are blocked: traffic to them is dropped (nothing changes in DNS). Servers it starts using are added as they show up, and an address stays blocked until 6 hours after it was last used.') ]),
			E('p', {}, [ E('small', {}, [ _('Big services share servers (CDNs): whatever else uses the same addresses stops too. Unblock it on the Blocked page.') ]) ]),
			E('p', { 'style': 'margin-top:12px;font-weight:600' }, [ _('Block it on which devices?') ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-negative', 'click': function() { go(g.key); } }, [ _('Only %s').format(name) ]), ' ',
				E('button', { 'class': 'btn cbi-button-negative', 'click': function() { go(''); } }, [ _('Every device') ]) ])
		]);
	},

	// over the connections of a service no list names: why it reads as it does, and Rename
	renameBar: function(s) {
		var self = this, why = /\snetwork$/.test(s.name) ? _('Named after who owns its address: no list knows this service.')
			: s.name == 'Other' ? _('Nothing names this traffic.')
			: _('Named after its domain: no list knows which app it belongs to.');
		return E('div', { 'style': 'display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin:6px 0 8px' }, [
			E('button', { 'class': 'ws-btn', 'type': 'button', 'click': function(ev) { ev.stopPropagation(); self.nameIt(s); } }, [ '✎ ', _('Rename…') ]),
			E('small', {}, [ why ]) ]);
	},

	// Rename: one of what this service's connections share (its domain, an address, a port) becomes a rule of
	// your own, and the live connections take the name at once
	nameIt: function(s) {
		var self = this, choices = [], seen = {};
		var add = function(key, value, label) { if (!seen[key + value]) { seen[key + value] = true; choices.push({ key: key, value: value, label: label }); } };
		s.flows.forEach(function(f) { if (f.site) add('domain', f.site, _('%s and its subdomains').format(f.site)); });
		s.flows.forEach(function(f) {
			var a = addrOf(f.remote);
			if (/^\d+\.\d+\.\d+\.\d+$/.test(a)) {
				add('cidr', a, _('the address %s').format(a) + (f.org ? ' (' + f.org + ')' : ''));
				add('cidr', a.replace(/\.\d+$/, '.0/24'), _('the addresses %s').format(a.replace(/\.\d+$/, '.0/24')));
			}
		});
		s.flows.forEach(function(f) {
			var p = portOf(f.remote);
			if (p && f.proto != 'icmp') add('port', f.proto + ':' + p, _('%s port %s (to any server)').format(f.proto.toUpperCase(), p));
		});
		if (!choices.length) return;
		var nameIn = E('input', { 'class': 'cbi-input-text', 'type': 'text', 'style': 'width:100%', 'maxlength': 64,
			'placeholder': _('for example Denuvo Anti-Cheat') });
		var kindSel = E('select', { 'class': 'cbi-input-select' }, KINDS().map(function(k) {
			return E('option', { 'value': k[0], 'selected': k[0] == (s.category && s.category != 'web' ? s.category : 'other') ? '' : null }, [ k[1] ]);
		}));
		var picks = choices.slice(0, 8).map(function(c, i) {
			return E('label', { 'style': 'display:block;margin:4px 0' }, [ E('input', { 'type': 'radio', 'name': 'ws-match', 'value': i, 'checked': i == 0 ? '' : null }), ' ', c.label ]);
		});
		var save = function() {
			var name = nameIn.value.trim();
			if (!name) { nameIn.focus(); return; }
			var i = +((picks.map(function(p) { return p.querySelector('input'); }).filter(function(r) { return r.checked; })[0] || {}).value || 0);
			var c = choices[i], o = { name: name, category: kindSel.value };
			o[c.key] = c.value;
			ws.setRule(o).then(function() {
				ui.hideModal();
				ui.addNotification(null, E('p', {}, [ _('“%s” now names %s. You can change it in Settings, under Your own services.').format(name, c.label) ]), 'info');
				return ws.flows('', '', 2000).then(function(r) { self.take(r); self.draw(true); });
			}).catch(function(err) { ui.addNotification(null, E('p', {}, [ err.message ]), 'danger'); });
		};
		ui.showModal(_('Rename %s').format(s.name), [
			E('p', {}, [ _('Give it a name of your own. It names this traffic from now on, on every device, and wins over the built-in names.') ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, [ _('Name') ]), E('div', { 'class': 'cbi-value-field' }, [ nameIn ]) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, [ _('Kind') ]), E('div', { 'class': 'cbi-value-field' }, [ kindSel ]) ]),
			E('div', { 'class': 'cbi-value' }, [ E('label', { 'class': 'cbi-value-title' }, [ _('What it matches') ]), E('div', { 'class': 'cbi-value-field' }, picks) ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-positive', 'click': save }, [ _('Save') ]) ])
		]);
		nameIn.addEventListener('keydown', function(ev) { if (ev.key == 'Enter') save(); });
		nameIn.focus();
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
