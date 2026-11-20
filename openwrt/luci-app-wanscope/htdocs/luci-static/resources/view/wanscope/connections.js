'use strict';
'require view';
'require poll';
'require wanscope.ui as ws';

// Network > wanscope > Connections: every open connection with its device, server name, service and uplink, live.
// The order is set when the page opens and when a column header is clicked; refreshes update the numbers in
// place, new connections join at the bottom and ended ones leave, so rows never jump around.

var SORTS = {
	dev: function(f) { return f.dev_name.toLowerCase(); },
	server: function(f) { return (f.host || f.remote).toLowerCase(); },
	service: function(f) { return (f.service || '').toLowerCase(); },
	wan: function(f) { return f.wan || ''; },
	rate: function(f) { return f.rate_down + f.rate_up; },
	total: function(f) { return f.down + f.up; },
	age: function(f) { return -f.first; }
};
// numbers sort largest first, words A to Z
var NUMERIC = { rate: true, total: true, age: true };

function idOf(f) { return f.id + '|' + f.local + '|' + f.remote; }

return view.extend({
	load: function() {
		return Promise.all([ ws.status(), ws.flows('', '', 1000) ]);
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.st = res[0] || {};
		this.flows = res[1].flows || [];
		this.total = res[1].total || 0;
		this.colors = ws.linkColors(this.st.ifaces);
		this.v = { q: '', dev: '', wan: '', sort: 'rate', lan: false };
		this.order = null;
		this.tableBox = E('div', { 'class': 'ws-scroll' });
		this.countBox = E('span', { 'class': 'ws-note' });
		this.devSel = E('select', { 'class': 'ws-in', 'change': function(ev) { self.v.dev = ev.target.value; self.draw(); } });
		this.wanSel = E('select', { 'class': 'ws-in', 'change': function(ev) { self.v.wan = ev.target.value; self.draw(); } });
		var root = E('div', { 'class': 'ws' }, [
			E('h2', {}, [ _('Connections') ]),
			E('p', { 'class': 'ws-sub' }, [ _('What every device is connected to right now. Names come from the server name the app sent (TLS or QUIC) or from the DNS answer it got. Click a column to sort by it.') ]),
			E('div', { 'class': 'ws-card' }, [
				E('div', { 'class': 'ws-bar' }, [
					E('input', { 'class': 'ws-in', 'type': 'search', 'placeholder': _('Find a server, service, address'), 'style': 'min-width:240px',
						'input': function(ev) { self.v.q = ev.target.value.toLowerCase(); self.draw(); } }),
					this.devSel, this.wanSel,
					E('label', { 'class': 'ws-note' }, [ E('input', { 'type': 'checkbox', 'change': function(ev) { self.v.lan = ev.target.checked; self.draw(); } }), ' ', _('show LAN-only') ]),
					E('span', { 'class': 'grow' }), this.countBox ]),
				this.tableBox ])
		]);
		this.draw();
		poll.add(function() {
			return ws.flows('', '', 1000).then(function(r) { self.flows = r.flows || []; self.total = r.total || 0; self.draw(); });
		}, 1);
		return root;
	},

	label: function(name) {
		var i = (this.st.ifaces || []).filter(function(x) { return x.name == name; })[0];
		return i ? i.label : name;
	},

	sortBy: function(key) {
		this.v.sort = this.v.sort == key ? key + '-rev' : key;
		this.order = null;
		this.draw();
	},

	draw: function() {
		var self = this, v = this.v, now = Date.now() / 1000;
		// selects keep their choice while the lists change
		var devs = {}, wans = {};
		this.flows.forEach(function(f) { if (f.dev) devs[f.dev] = f.dev_name || f.dev; if (f.wan) wans[f.wan] = true; });
		var fill = function(sel, items, cur, all) {
			sel.replaceChildren.apply(sel, [ E('option', { 'value': '' }, [ all ]) ].concat(items.map(function(it) {
				return E('option', { 'value': it[0], 'selected': it[0] == cur ? '' : null }, [ it[1] ]);
			})));
		};
		if (document.activeElement != this.devSel)
			fill(this.devSel, Object.keys(devs).sort(function(a, b) { return devs[a].localeCompare(devs[b]); }).map(function(k) { return [ k, devs[k] ]; }), v.dev, _('All devices'));
		if (document.activeElement != this.wanSel)
			fill(this.wanSel, Object.keys(wans).sort().map(function(k) { return [ k, self.label(k) ]; }), v.wan, _('All uplinks'));

		var byId = {};
		this.flows.forEach(function(f) { byId[idOf(f)] = f; });
		if (!this.order) {
			var s = v.sort.replace('-rev', ''), rev = /-rev$/.test(v.sort), key = SORTS[s];
			this.order = this.flows.slice().sort(function(a, b) {
				var x = key(a), y = key(b);
				var r = NUMERIC[s] ? y - x : x < y ? -1 : x > y ? 1 : 0;
				if (!r) r = (b.down + b.up) - (a.down + a.up);
				return rev ? -r : r;
			}).map(idOf);
		} else {
			// ended connections leave, new ones join at the bottom
			var known = {};
			this.order = this.order.filter(function(id) { known[id] = true; return byId[id]; });
			this.flows.forEach(function(f) { if (!known[idOf(f)]) self.order.push(idOf(f)); });
		}

		var list = this.order.map(function(id) { return byId[id]; }).filter(function(f) {
			if (!f) return false;
			if (!v.lan && f.lan) return false;
			if (v.dev && f.dev != v.dev) return false;
			if (v.wan && f.wan != v.wan) return false;
			if (v.q && ((f.host || '') + ' ' + f.remote + ' ' + (f.service || '') + ' ' + f.dev_name + ' ' + (f.proc || '')).toLowerCase().indexOf(v.q) < 0) return false;
			return true;
		});
		this.countBox.replaceChildren(_('%d of %d shown').format(Math.min(list.length, 300), this.total));
		list = list.slice(0, 300);

		var th = function(label, key, cls) {
			var on = v.sort.replace('-rev', '') == key, rev = /-rev$/.test(v.sort);
			var arrow = on ? ((NUMERIC[key] ? !rev : rev) ? ' ↓' : ' ↑') : '';
			return E('th', { 'class': cls || '', 'style': 'cursor:pointer;user-select:none', 'title': _('Sort'), 'click': function() { self.sortBy(key); } }, [ label, arrow ]);
		};
		this.tableBox.replaceChildren(list.length ? E('table', { 'class': 'ws-t' }, [
			E('tr', {}, [ th(_('Device'), 'dev'), th(_('Server'), 'server'), th(_('Service'), 'service'), th(_('Uplink'), 'wan'),
				th(_('Now'), 'rate', 'r'), th(_('In all'), 'total', 'r'), th(_('Open for'), 'age', 'r') ])
		].concat(list.map(function(f) {
			var busy = f.rate_down + f.rate_up > 0;
			return E('tr', { 'class': 'click', 'click': function() { location.href = L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(f.dev); } }, [
				E('td', {}, [ f.dev_name, f.proc ? E('div', {}, [ E('small', {}, [ f.proc ]) ]) : '' ]),
				E('td', { 'style': 'max-width:320px;overflow:hidden;text-overflow:ellipsis' }, [ f.host || f.remote, E('br'),
					E('small', {}, [ f.proto + ' ' + f.remote + (f.src ? ' · ' + ({ sni: _('TLS name'), quic: _('QUIC name'), dns: _('its DNS lookup'), 'dns*': _('another device\'s DNS lookup') }[f.src] || f.src) : '') ]) ]),
				E('td', {}, [ f.service ? E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'margin-right:6px;background:' + ws.catColor(f.category) }), f.service ]) : '' ]),
				E('td', {}, [ f.wan ? E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'margin-right:6px;background:' + (self.colors[f.wan] || 'var(--ws-other)') }), self.label(f.wan) ]) : f.lan ? E('small', {}, [ _('LAN') ]) : '—' ]),
				E('td', { 'class': 'r' }, busy ? [ '↓ ' + ws.rate(f.rate_down), E('br'), E('small', {}, [ '↑ ' + ws.rate(f.rate_up) ]) ] : [ E('small', {}, [ _('idle %s').format(ws.dur(now - f.last)) ]) ]),
				E('td', { 'class': 'r' }, [ ws.bytes(f.down + f.up), E('br'), E('small', {}, [ '↓ ' + ws.bytes(f.down) + ' ↑ ' + ws.bytes(f.up) ]) ]),
				E('td', { 'class': 'r' }, [ ws.dur(now - f.first) ])
			]);
		}))) : E('div', { 'class': 'ws-empty' }, [ _('No connections match.') ]));
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
