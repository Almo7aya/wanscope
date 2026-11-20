'use strict';
'require view';
'require poll';
'require wanscope.ui as ws';

// Network > wanscope > Overview: what flows right now (per uplink, live), who uses it, and today so far.

var WINDOWS = [ [ 60, _('1 min') ], [ 300, _('5 min') ], [ 900, _('15 min') ], [ 3600, _('1 h') ] ];

return view.extend({
	load: function() {
		var now = Math.floor(Date.now() / 1000), day = new Date(); day.setHours(0, 0, 0, 0);
		return Promise.all([ ws.status(), ws.live(0, 300), ws.devices(), ws.wans(), ws.top([ '*' ], Math.floor(day / 1000), now + 1, 12), ws.plans() ]);
	},

	render: function(res) {
		ws.css();
		var st = res[0] || {}, state = { win: 300, hovering: false, ok: true, lastOk: Date.now() };
		var buf = { from: 0, to: 0, ifaces: {}, devices: {} };
		var devs = res[2].devices || [], wans = res[3].wans || [], top = res[4], plans = (res[5] || {}).plans || [];

		var uplinks = (st.ifaces || []).filter(function(i) { return i.role == 'wan' && i.kind == 'uplink' && !i.hidden; });
		var colors = ws.linkColors(st.ifaces);
		var label = function(name) { var i = (st.ifaces || []).filter(function(x) { return x.name == name; })[0]; return i ? i.label : name; };

		// keep the 1-second points of the window, aligned on one timeline
		var merge = function(lv) {
			if (!lv || !lv.to) return;
			if (lv.from > buf.to + 1 || !buf.to) { buf = { from: lv.from, to: lv.from - 1, ifaces: {}, devices: {} }; }
			var add = function(store, data) {
				for (var k in data) {
					var p = ws.unpack(data[k]);
					if (!store[k]) store[k] = { down: new Array(buf.to - buf.from + 1).fill(0), up: new Array(buf.to - buf.from + 1).fill(0) };
					var s = store[k], off = lv.from - buf.from;
					for (var i = 0; i < p.down.length; i++) { s.down[off + i] = p.down[i]; s.up[off + i] = p.up[i]; }
				}
			};
			var newLen = lv.to - buf.from + 1;
			[ buf.ifaces, buf.devices ].forEach(function(store) {
				for (var k in store) while (store[k].down.length < newLen) { store[k].down.push(0); store[k].up.push(0); }
			});
			buf.to = lv.to;
			add(buf.ifaces, lv.ifaces);
			add(buf.devices, lv.devices);
			var keep = 3600, cut = buf.to - buf.from + 1 - keep;
			if (cut > 0) {
				buf.from += cut;
				[ buf.ifaces, buf.devices ].forEach(function(store) {
					for (var k in store) { store[k].down.splice(0, cut); store[k].up.splice(0, cut); }
				});
			}
		};
		merge(res[1]);

		var avg = function(arr, n) { var s = 0, k = 0; for (var i = Math.max(0, arr.length - n); i < arr.length; i++) { s += arr[i]; k++; } return k ? s / k : 0; };

		var root = E('div', { 'class': 'ws' });
		var warn = E('div');
		var head = E('div', { 'class': 'ws-now' }), share = E('div', { 'class': 'ws-share' }), legend = E('div', { 'class': 'ws-legend' });
		var winBox = E('span'), liveBox = E('span');
		var plotDown = E('div'), plotUp = E('div');
		var devBox = E('div'), wanBox = E('div'), svcBox = E('div'), topDevBox = E('div');

		root.appendChild(E('h2', {}, [ _('wanscope') ]));
		root.appendChild(E('p', { 'class': 'ws-sub' }, [ _('Traffic through this router: by uplink, by device and by service.') ]));
		root.appendChild(warn);
		root.appendChild(E('div', { 'class': 'ws-card' }, [
			E('h3', {}, [ _('Right now'), E('span', { 'class': 'grow' }), liveBox, winBox ]),
			head, share, legend,
			E('div', { 'class': 'ws-plot-label' }, [ E('span', {}, [ _('Download by uplink') ]) ]), plotDown,
			E('div', { 'class': 'ws-plot-label' }, [ E('span', {}, [ _('Upload by uplink') ]) ]), plotUp
		]));
		root.appendChild(E('div', { 'class': 'ws-cols' }, [
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Devices now'), E('small', {}, [ _('last 5 seconds') ]) ]), devBox ]),
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Data left and uplinks today'), E('span', { 'class': 'grow' }),
				E('a', { 'href': L.url('admin/network/wanscope/uplinks') }, [ _('All uplinks') ]) ]), wanBox ])
		]));
		root.appendChild(E('div', { 'class': 'ws-cols' }, [
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('What used the internet today') ]), svcBox ]),
			E('div', { 'class': 'ws-card' }, [ E('h3', {}, [ _('Busiest devices today'), E('span', { 'class': 'grow' }),
				E('a', { 'href': L.url('admin/network/wanscope/devices') }, [ _('All devices') ]) ]), topDevBox ])
		]));

		var devName = function(k) { var d = devs.filter(function(x) { return x.key == k; })[0]; return d ? d.name : k.replace(/^ip:/, ''); };
		var devURL = function(k) { return L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(k); };

		var drawWarn = function() {
			var w = (st.warnings || []).slice();
			if (st.policy_error) w.push(_('Limits could not be applied: %s').format(st.policy_error));
			warn.replaceChildren.apply(warn, w.map(function(t) { return E('div', { 'class': 'ws-warnbox' }, [ t ]); }));
		};

		var drawLive = function() {
			var n = buf.to - buf.from + 1;
			if (n <= 0) return;
			var take = Math.min(state.win, n), off = n - take, t0 = buf.to - take + 1;
			var down = 0, up = 0, parts = [];
			uplinks.forEach(function(u) {
				var s = buf.ifaces[u.name];
				var d = s ? avg(s.down, 3) : 0, p = s ? avg(s.up, 3) : 0;
				down += d; up += p;
				parts.push({ u: u, d: d, p: p });
			});
			var online = devs.filter(function(d) { return d.online && d.key != 'router'; }).length;
			var today = wans.filter(function(w) { return w.kind == 'uplink'; }).reduce(function(s, w) { return s + (w.today ? w.today.down + w.today.up : 0); }, 0);
			var rd = ws.rateParts(down), ru = ws.rateParts(up), bt = ws.bytesParts(today);
			head.replaceChildren(
				E('div', {}, [ E('div', { 'class': 'lab' }, [ _('Download') ]), E('div', { 'class': 'big num' }, [ rd[0], E('small', {}, [ rd[1] ]) ]) ]),
				E('div', {}, [ E('div', { 'class': 'lab' }, [ _('Upload') ]), E('div', { 'class': 'big num' }, [ ru[0], E('small', {}, [ ru[1] ]) ]) ]),
				E('div', { 'class': 'side' }, [
					E('div', {}, [ E('b', { 'class': 'num' }, [ bt[0] + ' ' + bt[1] ]), ' ', _('through the uplinks today') ]),
					E('div', {}, [ E('b', { 'class': 'num' }, [ String(online) ]), ' ', _('devices online') ]) ])
			);
			var tot = down + up;
			share.replaceChildren.apply(share, parts.filter(function(x) { return x.d + x.p > 0; }).map(function(x) {
				return E('span', { 'style': 'flex-grow:' + ((x.d + x.p) / (tot || 1)).toFixed(4) + ';background:' + colors[x.u.name],
					'title': x.u.label + ': ' + ws.rate(x.d + x.p) });
			}));
			legend.replaceChildren.apply(legend, parts.map(function(x) {
				return E('span', { 'title': _('download %s, upload %s').format(ws.rate(x.d), ws.rate(x.p)) }, [
					E('span', { 'class': 'ws-key', 'style': 'background:' + colors[x.u.name] }), x.u.label + (x.u.up ? '' : ' (' + _('down') + ')'),
					E('b', { 'class': 'num' }, [ ws.rate(x.d) ]) ]);
			}));
			if (!ws.hovering()) {
				var W = ws.plotWidth(plotDown);
				[ [ plotDown, 'down', 170 ], [ plotUp, 'up', 110 ] ].forEach(function(p) {
					var layers = uplinks.map(function(u) {
						var s = buf.ifaces[u.name];
						return { label: u.label, color: colors[u.name], vals: s ? s[p[1]].slice(off) : new Array(take).fill(0) };
					});
					var ch = ws.areas({ W: W, H: p[2], t0: t0, step: 1, n: take, layers: layers, minMax: 15000,
						fmt: function(v, full) { var p = ws.rateParts(v); return full ? p.join(' ') : p[0] + ' ' + p[1].replace('bit/s', ''); },
						title: p[1] == 'down' ? _('download') : _('upload'),
						onHover: function(i) { state.hovering = i != null; } });
					p[0].replaceChildren(ch.svg);
				});
			}
			// devices by the last 5 seconds
			var rows = Object.keys(buf.devices).map(function(k) {
				var s = buf.devices[k];
				return { key: k, d: avg(s.down, 5), p: avg(s.up, 5) };
			}).filter(function(r) { return r.d + r.p > 0; }).sort(function(a, b) { return b.d + b.p - (a.d + a.p); }).slice(0, 10);
			devBox.replaceChildren(ws.rows(rows.map(function(r) {
				var d = devs.filter(function(x) { return x.key == r.key; })[0] || { key: r.key, name: devName(r.key) };
				return { label: d.name, icon: ws.icon(d), sub: (d.top || [])[0] || '', down: r.d, up: r.p, color: ws.devColor(r.key),
					value: ws.rate(r.d + r.p), vsub: '↑ ' + ws.rate(r.p), click: function() { location.href = devURL(r.key); }, chips: ws.chips(d) };
			}), { rate: true, empty: _('Nothing is moving right now.') }));
		};

		var drawToday = function() {
			var list = wans.filter(function(w) { return w.today && (w.today.down + w.today.up > 0 || w.up); });
			var planRows = plans.map(function(p) {
				var pct = p.size ? p.used / p.size : 0, color = pct >= 1 ? 'var(--ws-bad)' : pct >= 0.8 ? 'var(--ws-warn)' : 'var(--ws-good)';
				return E('div', { 'class': 'ws-row click', 'click': function() { location.href = L.url('admin/network/wanscope/uplinks'); } }, [
					E('div', { 'class': 'n' }, [ E('span', { 'class': 't', 'style': 'font-weight:600' }, [ p.name ]), E('small', {}, [ (p.ifaces || []).join(', ') ]) ]),
					E('div', { 'class': 'v' }, [ E('b', {}, [ ws.bytes(Math.max(0, p.left)) ]), ' ', _('left'), E('small', {}, [ p.runs_out && p.left > 0 ? _('until ~%s').format(ws.dayLabel(p.runs_out)) : p.left <= 0 ? _('used up') : '' ]) ]),
					E('div', { 'style': 'grid-column:1/3' }, [ E('div', { 'class': 'ws-meter', 'style': 'margin:2px 0' }, [ E('span', { 'style': 'width:' + Math.min(100, pct * 100).toFixed(1) + '%;background:' + color }) ]) ])
				]);
			});
			wanBox.replaceChildren(planRows.length ? E('div', { 'class': 'ws-rows', 'style': 'margin-bottom:12px' }, planRows) : '', list.length ? E('div', { 'class': 'ws-rows' }, list.map(function(w) {

				return E('div', { 'class': 'ws-row click', 'click': function() { location.href = L.url('admin/network/wanscope/uplinks') + '#' + w.name; } }, [
					E('div', { 'class': 'n' }, [ E('span', { 'class': 'ws-key', 'style': 'background:' + colors[w.name] }), E('span', { 'class': 't' }, [ w.label ]),
						E('small', {}, [ w.kind == 'tunnel' ? _('tunnel') : w.up ? '' : _('offline') ]) ]),
					E('div', { 'class': 'v' }, [ ws.bytes(w.today.down + w.today.up), E('small', {}, [ _('today') ]) ]),
					E('div', { 'class': 'b' }, [ E('span', { 'style': 'width:' + Math.min(100, (w.today.down + w.today.up) / Math.max.apply(null, list.map(function(x) { return x.today.down + x.today.up; }).concat([ 1 ])) * 100) + '%;background:' + colors[w.name] }) ])
				]);
			})) : E('div', { 'class': 'ws-empty' }, [ _('No uplink traffic yet today.') ]));

			var svcs = ((top.all || {}).services || []).slice(0, 10);
			svcBox.replaceChildren(ws.rows(svcs.map(function(s) {
				return { label: s.name, sub: s.category && s.category != 'other' ? s.category : '', down: s.down, up: s.up, color: catColor(s.category) };
			}), { empty: _('No traffic named yet. Names come from DNS answers and TLS server names as devices connect.') }));

			var td = devs.filter(function(d) { return d.today.down + d.today.up > 0; }).slice(0, 8);
			topDevBox.replaceChildren(ws.rows(td.map(function(d) {
				return { label: d.name, icon: ws.icon(d), sub: (d.top || []).join(', '), down: d.today.down, up: d.today.up, color: ws.devColor(d.key),
					click: function() { location.href = devURL(d.key); }, chips: ws.chips(d) };
			})));
		};

		var catColor = function(c) { return ws.catColor(c); };

		var drawWin = function() {
			winBox.replaceChildren(ws.seg(WINDOWS, state.win, function(v) {
				state.win = v;
				drawWin();
				if (v > buf.to - buf.from + 1)
					ws.live(buf.to - v, v).then(function(lv) { buf = { from: 0, to: 0, ifaces: {}, devices: {} }; merge(lv); drawLive(); });
				else drawLive();
			}));
		};

		drawWarn(); drawWin(); drawLive(); drawToday();
		liveBox.replaceChildren(ws.liveDot(true));

		// live points and devices every second, uplink totals every 2 s, the service breakdown every 5 s
		var tick = 0;
		poll.add(function() {
			tick++;
			var now = Math.floor(Date.now() / 1000), day = new Date(); day.setHours(0, 0, 0, 0);
			return Promise.all([
				ws.live(buf.to, state.win),
				ws.devices(),
				tick % 2 == 0 ? ws.wans() : null,
				tick % 5 == 0 ? ws.top([ '*' ], Math.floor(day / 1000), now + 1, 12) : null,
				tick % 30 == 0 ? ws.status() : null,
				tick % 2 == 0 ? ws.plans() : null
			]).then(function(r) {
				if (r[0] && r[0].to) { merge(r[0]); state.lastOk = Date.now(); }
				if (r[1] && r[1].devices) devs = r[1].devices;
				if (r[2] && r[2].wans) wans = r[2].wans;
				if (r[3]) top = r[3];
				if (r[4]) { st = r[4]; drawWarn(); }
				if (r[5] && r[5].plans) plans = r[5].plans;
				drawLive();
				drawToday();
				liveBox.replaceChildren(ws.liveDot(Date.now() - state.lastOk < 5000));
			});
		}, 1);
		return root;
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
