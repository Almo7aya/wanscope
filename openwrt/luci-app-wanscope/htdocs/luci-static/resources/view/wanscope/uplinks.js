'use strict';
'require view';
'require poll';
'require ui';
'require wanscope.ui as ws';

// Network > wanscope > Uplinks: data plans (what is left of each bundle, the pace, when it runs out), then each
// WAN / SIM and the tunnels with what carries them.

function gb(b) { return +(b / 1e9).toFixed(b >= 1e10 ? 1 : 2); }
function when(t) { return new Date(t * 1000).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }); }
function day(t) { return new Date(t * 1000).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' }); }
// a span of history, roughly: "4 hours", "3 days", "7 days"
function span(s) {
	if (s < 86400) { var h = Math.max(1, Math.round(s / 3600)); return h == 1 ? _('hour') : _('%d hours').format(h); }
	var d = Math.min(7, Math.round(s / 86400)); return d == 1 ? _('day') : _('%d days').format(d);
}

return view.extend({
	load: function() {
		return Promise.all([ ws.status(), ws.wans(), ws.plans() ]);
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.st = res[0] || {};
		this.wans = res[1].wans || [];
		this.plans = res[2].plans || [];
		this.colors = ws.linkColors(this.st.ifaces);
		this.root = E('div', { 'class': 'ws' });
		this.draw();
		var tick = 0;
		poll.add(function() {
			tick++;
			return Promise.all([ ws.wans(), ws.plans(), tick % 30 == 0 ? ws.status() : null ]).then(function(r) {
				self.wans = r[0].wans || self.wans;
				self.plans = r[1].plans || self.plans;
				if (r[2]) self.st = r[2];
				if (!ws.hovering()) self.draw();
			});
		}, 1);
		return this.root;
	},

	label: function(name) {
		var w = this.wans.filter(function(x) { return x.name == name; })[0];
		return w ? w.label : name;
	},

	reload: function(msg) {
		var self = this;
		return Promise.all([ ws.wans(), ws.plans(), ws.status() ]).then(function(r) {
			self.wans = r[0].wans || self.wans; self.plans = r[1].plans || self.plans; self.st = r[2] || self.st;
			self.draw();
			if (msg) ui.addNotification(null, E('p', {}, [ msg ]), 'info');
		});
	},

	fail: function(e) { ui.addNotification(null, E('p', {}, [ _('Not saved: %s').format(e.message) ]), 'error'); },

	draw: function() {
		var self = this;
		var tot = function(c) { return c ? c.down + c.up : 0; };
		var uplinks = this.wans.filter(function(w) { return w.kind == 'uplink'; });
		var tunnels = this.wans.filter(function(w) { return w.kind == 'tunnel'; });
		var all = uplinks.reduce(function(s, w) { return s + tot(w.today); }, 0);
		var focus = location.hash.replace('#', '');
		this.pending = [];

		var keyOf = function(n) { return E('span', { 'style': 'display:inline-flex;align-items:center;gap:5px;margin-right:10px' }, [
			E('span', { 'class': 'ws-key', 'style': 'background:' + (self.colors[n] || 'var(--ws-other)') }), self.label(n) ]); };

		var planCard = function(p) {
			var now = Date.now() / 1000, used = p.used, left = p.left, pct = p.size ? used / p.size : 0;
			var color = pct >= 1 ? 'var(--ws-bad)' : pct >= 0.8 ? 'var(--ws-warn)' : 'var(--ws-good)';
			var lp = ws.bytesParts(Math.max(0, left));
			var since = p.type == 'monthly' ? _('this cycle, since %s; it renews %s').format(day(p.start), day(p.end))
				: _('since you bought it on %s (%s ago)').format(when(p.start), ws.dur(now - p.start));
			// the pace is the average over the history there is, up to 7 days (pace_from)
			var over = p.pace_from ? span(now - p.pace_from) : '';
			var pace = p.pace > 0 ? _('Lately %s a day (over the last %s).').format(ws.bytes(p.pace), over) : '';
			var out = left <= 0 ? _('All used.') : p.runs_out ? _('At that pace it runs out around %s.').format(when(p.runs_out)) : '';
			var expires = p.type == 'prepaid' && p.end ? _('The bundle expires %s.').format(when(p.end)) : '';
			var days = ws.unpack(p.days), bins = [];
			for (var i = 0; i < days.down.length; i++) bins.push(p.days_from + i * 86400);
			var hist = (p.history || []).slice(0, 5);
			return E('div', { 'class': 'ws-card' }, [
				E('h3', {}, [ p.name, E('small', {}, [ p.type == 'monthly' ? _('monthly, renews on day %d').format(p.cycle_day) : _('prepaid') ]),
					E('span', { 'class': 'grow' }),
					p.type == 'prepaid' ? E('button', { 'class': 'ws-btn on', 'click': function() { self.topup(p); } }, [ _('I bought more') ]) : '',
					E('button', { 'class': 'ws-btn', 'click': function() { self.calibrate(p); } }, [ _('Correct it') ]),
					E('button', { 'class': 'ws-btn', 'click': function() { self.editPlan(p); } }, [ _('Edit') ]) ]),
				E('div', { 'class': 'ws-note', 'style': 'margin:-4px 0 10px' }, (p.ifaces || []).map(keyOf)),
				E('div', { 'class': 'ws-now', 'style': 'margin-bottom:4px' }, [
					E('div', {}, [ E('div', { 'class': 'lab' }, [ _('Left') ]), E('div', { 'class': 'big num', 'style': 'color:' + (left <= 0 ? 'var(--ws-bad)' : 'inherit') }, [ lp[0], E('small', {}, [ lp[1] ]) ]) ]),
					E('div', { 'class': 'side' }, [
						E('div', {}, [ E('b', { 'class': 'num' }, [ ws.bytes(used) ]), ' ', _('of %s used').format(ws.bytes(p.size)), ' ', since ]),
						E('div', {}, [ [ pace, out, expires ].filter(Boolean).join(' ') ]),
						p.adjust ? E('div', { 'class': 'ws-note' }, [ _('Includes a correction of %s to match the carrier.').format((p.adjust > 0 ? '+' : '−') + ws.bytes(Math.abs(p.adjust))) ]) : '' ]) ]),
				E('div', { 'class': 'ws-meter', 'style': 'height:14px;border-radius:7px', 'role': 'meter', 'aria-valuenow': Math.round(pct * 100), 'aria-valuemin': 0, 'aria-valuemax': 100 }, [
					E('span', { 'style': 'border-radius:7px;width:' + Math.min(100, pct * 100).toFixed(2) + '%;background:' + color }) ]),
				E('div', { 'class': 'ws-tiles', 'style': 'margin-top:12px' }, [
					ws.tile(_('Runs out'), left <= 0 ? _('All used') : p.runs_out ? '~' + day(p.runs_out) : '—', '',
						left <= 0 ? '' : p.runs_out ? _('at the pace of the last %s').format(over) : _('after an hour of history')),
					ws.tile(_('Today on these uplinks'), ws.bytesParts(tot(p.today))[0], ws.bytesParts(tot(p.today))[1], '↓ ' + ws.bytes(p.today.down) + '  ↑ ' + ws.bytes(p.today.up)),
					ws.tile(_('Per day lately'), ws.bytesParts(p.pace)[0], ws.bytesParts(p.pace)[1], over ? _('average of the last %s').format(over) : ''),
					ws.tile(_('Used'), Math.round(pct * 100) + '', '%', ws.bytes(used) + ' / ' + ws.bytes(p.size))
				]),
				bins.length > 1 ? E('div', { 'class': 'ws-plot-label' }, [ _('Each day of this bundle') ]) : '',
				bins.length > 1 ? self.later(function(W) { return ws.bars({ W: W, H: 110, bins: bins, layers: [
					{ label: _('download'), color: 'var(--ws-c1)', vals: days.down },
					{ label: _('upload'), color: 'color-mix(in srgb, var(--ws-c1) 45%, transparent)', vals: days.up } ],
					fmt: function(v) { return ws.bytes(v); }, fmtBin: function(t) { return String(new Date(t * 1000).getDate()); }, fmtTip: function(t) { return ws.dayLabel(t); } }); }) : '',
				hist.length ? E('div', { 'style': 'margin-top:10px' }, [ E('div', { 'class': 'ws-plot-label' }, [ _('Bought') ]),
					E('div', { 'class': 'ws-rows' }, hist.map(function(h) {
						return E('div', { 'class': 'ws-row' }, [
							E('div', { 'class': 'n' }, [ E('span', { 'class': 't' }, [ when(h.t) + ': ' + ws.bytes(h.size_gb * 1e9) + (h.mode == 'add' ? ' ' + _('added to %s left').format(ws.bytes(Math.max(0, h.left))) : '') ]) ]),
							E('div', { 'class': 'v' }, [ E('small', {}, [ _('the one before lasted %s and used %s').format(ws.dur(h.lasted), ws.bytes(h.used)) ]) ]) ]);
					})) ]) : ''
			]);
		};

		var card = function(w) {
			var color = self.colors[w.name] || 'var(--ws-other)';
			var days = ws.unpack(w.days), bins = [];
			for (var i = 0; i < days.down.length; i++) bins.push(w.days_from + i * 86400);
			var month = days.down.reduce(function(s, v, i) { return s + v + days.up[i]; }, 0);
			var carries = Object.keys(w.carries || {}).filter(function(k) { return tot(w.carries[k]) > 0; });
			var rate = w.rate || { down: 0, up: 0 };
			return E('div', { 'class': 'ws-card', 'id': 'wan-' + w.name, 'style': focus == w.name ? 'outline:2px solid ' + color : '' }, [
				E('h3', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:' + color }), w.label,
					E('small', {}, [ [ w.label != w.name ? w.name : '', w.dev, w.gateway ? _('via %s').format(w.gateway) : '' ].filter(Boolean).join(' · ') ]),
					w.up ? '' : E('span', { 'class': 'ws-chip bad' }, [ _('offline') ]),
					w.plan_name ? E('span', { 'class': 'ws-chip' }, [ _('plan: %s').format(w.plan_name) ]) : '',
					E('span', { 'class': 'grow' }),
					E('button', { 'class': 'ws-btn', 'click': function() { self.editName(w); } }, [ _('Name') ]) ]),
				E('div', { 'class': 'ws-tiles' }, [
					ws.tile(_('Now'), ws.rateParts(rate.down)[0], ws.rateParts(rate.down)[1], '↑ ' + ws.rate(rate.up)),
					ws.tile(_('Today'), ws.bytesParts(tot(w.today))[0], ws.bytesParts(tot(w.today))[1], all ? _('%d%% of all uplinks').format(Math.round(tot(w.today) / all * 100)) : ''),
					ws.tile(_('Last 30 days'), ws.bytesParts(month)[0], ws.bytesParts(month)[1])
				]),
				bins.length ? E('div', { 'class': 'ws-plot-label' }, [ _('Each of the last 30 days') ]) : '',
				bins.length ? self.later(function(W) { return ws.bars({ W: W, H: 110, bins: bins, layers: [
					{ label: _('download'), color: color, vals: days.down },
					{ label: _('upload'), color: 'color-mix(in srgb, ' + color + ' 45%, transparent)', vals: days.up } ],
					fmt: function(v) { return ws.bytes(v); }, fmtBin: function(t) { return String(new Date(t * 1000).getDate()); }, fmtTip: function(t) { return ws.dayLabel(t); } }); }) : '',
				E('div', { 'class': 'ws-cols', 'style': 'margin-top:12px' }, [
					E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('Devices today') ]),
						ws.rows((w.devices || []).map(function(d) {
							return { label: d.name, down: d.down, up: d.up, color: ws.devColor(d.category),
								click: function() { location.href = L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(d.category); } };
						}), { empty: _('Nobody used it today.') }) ]),
					E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('Services today') ]),
						ws.rows((w.services || []).map(function(s) { return { label: s.name, down: s.down, up: s.up, color: ws.catColor(s.category) }; })) ])
				]),
				carries.length ? E('div', { 'class': 'ws-note', 'style': 'margin-top:10px' }, [
					_('Tunnel transport today: %s. That is the tunnels\' traffic again, wrapped: it counts on this uplink\'s data too.').format(
						carries.map(function(k) { return self.label(k) + ' ' + ws.bytes(tot(w.carries[k])); }).join(', ')) ]) : ''
			]);
		};

		var tunnelCard = function(w) {
			var via = Object.keys(w.carried || {}).filter(function(k) { return tot(w.carried[k]) > 0; });
			var carriedTot = via.reduce(function(s, k) { return s + tot(w.carried[k]); }, 0);
			var inner = tot(w.today);
			return E('div', { 'class': 'ws-card', 'id': 'wan-' + w.name }, [
				E('h3', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:var(--ws-other)' }), w.label, E('small', {}, [ _('tunnel') + ' · ' + w.dev ]),
					w.up ? '' : E('span', { 'class': 'ws-chip bad' }, [ _('offline') ]), E('span', { 'class': 'grow' }),
					E('button', { 'class': 'ws-btn', 'click': function() { self.editName(w); } }, [ _('Name') ]) ]),
				E('div', { 'class': 'ws-tiles' }, [
					ws.tile(_('Now'), ws.rateParts((w.rate || {}).down)[0], ws.rateParts((w.rate || {}).down)[1], '↑ ' + ws.rate((w.rate || {}).up)),
					ws.tile(_('Inside the tunnel today'), ws.bytesParts(inner)[0], ws.bytesParts(inner)[1]),
					ws.tile(_('On the uplinks'), ws.bytesParts(carriedTot)[0], ws.bytesParts(carriedTot)[1],
						inner && carriedTot ? _('%s× the traffic inside').format((carriedTot / inner).toFixed(2)) : _('transport not seen'))
				]),
				via.length ? E('div', { 'class': 'ws-legend' }, via.map(function(k) {
					return E('span', {}, [ E('span', { 'class': 'ws-key', 'style': 'background:' + (self.colors[k] || 'var(--ws-other)') }), _('over %s').format(self.label(k)), E('b', {}, [ ws.bytes(tot(w.carried[k])) ]) ]);
				})) : E('div', { 'class': 'ws-note' }, [ _('wanscope finds a tunnel\'s transport from WireGuard peer endpoints and the program relaying it.') ]),
				E('div', { 'class': 'ws-cols', 'style': 'margin-top:10px' }, [
					E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('Devices today') ]),
						ws.rows((w.devices || []).map(function(d) { return { label: d.name, down: d.down, up: d.up, color: ws.devColor(d.category) }; }), { empty: _('No device traffic through it today.') }) ]),
					E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('Services today') ]),
						ws.rows((w.services || []).map(function(s) { return { label: s.name, down: s.down, up: s.up, color: ws.catColor(s.category) }; })) ])
				])
			]);
		};

		this.root.replaceChildren.apply(this.root, [
			E('h2', {}, [ _('Uplinks') ]),
			E('p', { 'class': 'ws-sub' }, [ _('Data through each uplink, counted on the link itself (IP bytes, what carriers count), and what is left of each data plan.') ]),
			E('div', { 'class': 'ws-bar', 'style': 'margin:4px 0 10px' }, [ E('h3', { 'style': 'margin:0' }, [ _('Data plans') ]), E('span', { 'class': 'grow' }),
				E('button', { 'class': 'ws-btn', 'click': function() { self.editPlan(null); } }, [ _('Add a data plan') ]) ])
		].concat(this.plans.length ? this.plans.map(planCard) : [ E('div', { 'class': 'ws-card ws-empty' }, [ _('No data plans yet. Add one to see how much of a bundle is left and when it runs out.') ]) ])
			.concat([ E('h3', { 'style': 'margin:22px 0 10px' }, [ _('Uplinks') ]) ]).concat(uplinks.map(card))
			.concat(tunnels.length ? [ E('h3', { 'style': 'margin:22px 0 10px' }, [ _('Tunnels') ]) ] : []).concat(tunnels.map(tunnelCard)));
		var todo = this.pending;
		requestAnimationFrame(function() { todo.forEach(function(p) { p.node.replaceChildren(p.fn(ws.plotWidth(p.node, 640))); }); });
	},

	// a chart drawn once its box is on the page and has a width
	later: function(fn) {
		var node = E('div', { 'style': 'min-height:110px' });
		this.pending.push({ node: node, fn: fn });
		return node;
	},

	editName: function(w) {
		var self = this;
		var label = E('input', { 'class': 'cbi-input-text', 'value': w.label != w.name ? w.label : '', 'placeholder': w.name });
		ui.showModal(_('Name for %s').format(w.name), [
			E('div', { 'class': 'ws' }, [ label, E('p', { 'class': 'ws-note' }, [ _('Shown instead of %s on every page. Empty goes back to %s.').format(w.name, w.name) ]) ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					ui.hideModal();
					ws.setWan(w.name, { label: label.value.trim() }).then(function() { return self.reload(_('Saved.')); }).catch(function(e) { self.fail(e); });
				} }, [ _('Save') ]) ]) ]);
		label.focus();
	},

	editPlan: function(p) {
		var self = this, isNew = !p;
		p = p || { name: '', type: 'prepaid', size: 60e9, ifaces: [], cycle_day: 1, valid_days: 0 };
		var name = E('input', { 'class': 'cbi-input-text', 'value': p.name, 'placeholder': _('e.g. Router 1 SIM') });
		var size = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 0.1, 'step': 0.1, 'value': gb(p.size), 'style': 'width:8em' });
		var type = E('select', { 'class': 'cbi-input-select', 'change': function() { show(); } }, [
			E('option', { 'value': 'prepaid', 'selected': p.type == 'prepaid' ? '' : null }, [ _('Prepaid: I buy a bundle, use it up, buy again') ]),
			E('option', { 'value': 'monthly', 'selected': p.type == 'monthly' ? '' : null }, [ _('Monthly: renews on the same day each month') ]) ]);
		var cday = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 1, 'max': 28, 'value': p.cycle_day || 1, 'style': 'width:6em' });
		var valid = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 0, 'value': p.valid_days || '', 'placeholder': _('never'), 'style': 'width:6em' });
		var usedNow = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 0, 'step': 0.1, 'placeholder': '0', 'style': 'width:8em' });
		var uplinks = this.wans.filter(function(w) { return w.kind == 'uplink'; });
		var boxes = uplinks.map(function(w) {
			return E('label', { 'style': 'margin-right:14px;white-space:nowrap' }, [ E('input', { 'type': 'checkbox', 'value': w.name, 'checked': (p.ifaces || []).indexOf(w.name) >= 0 ? '' : null }), ' ', w.label,
				w.plan && w.plan != p.id ? E('small', { 'class': 'ws-note' }, [ ' (' + _('now in %s').format(w.plan_name) + ')' ]) : '' ]);
		});
		var monthlyRow = E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('Renews on day') ]), cday ]);
		var prepaidRow = E('div', {}, [ E('div', { 'class': 'ws-plot-label' }, [ _('The bundle expires after (days)') ]), valid ].concat(isNew ? [
			E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:10px' }, [ _('Already used of the current bundle (GB)') ]), usedNow,
			E('div', { 'class': 'ws-note' }, [ _('If it isn\'t new, say how much the carrier shows as used; or use "Correct it" later.') ]) ] : []));
		var show = function() { monthlyRow.style.display = type.value == 'monthly' ? '' : 'none'; prepaidRow.style.display = type.value == 'prepaid' ? '' : 'none'; };
		show();
		ui.showModal(isNew ? _('Add a data plan') : _('Edit %s').format(p.name), [
			E('div', { 'class': 'ws' }, [
				E('div', { 'class': 'ws-plot-label' }, [ _('Name') ]), name,
				E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:10px' }, [ _('Kind') ]), type,
				E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:10px' }, [ _('Size (GB)') ]), size,
				E('div', { 'class': 'ws-plot-label', 'style': 'margin-top:10px' }, [ _('Uplinks that use it (SIMs in the same modem share one plan)') ]), E('div', {}, boxes),
				E('div', { 'style': 'margin-top:10px' }, [ monthlyRow, prepaidRow ]) ]),
			E('div', { 'class': 'right', 'style': 'margin-top:14px' }, [
				isNew ? '' : E('button', { 'class': 'btn cbi-button-negative', 'style': 'float:left', 'click': function() {
					ui.hideModal();
					ws.deletePlan(p.id).then(function() { return self.reload(_('Plan removed.')); }).catch(function(e) { self.fail(e); });
				} }, [ _('Remove plan') ]),
				E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					var ifaces = boxes.map(function(b) { return b.firstChild; }).filter(function(c) { return c.checked; }).map(function(c) { return c.value; });
					ui.hideModal();
					ws.setPlan({ id: isNew ? '' : p.id, name: name.value.trim(), type: type.value, size_gb: String(+size.value || 0), ifaces: ifaces,
						cycle_day: +cday.value || 1, valid_days: +valid.value || 0, used_gb: isNew ? String(+usedNow.value || 0) : undefined })
						.then(function() { return self.reload(_('Saved.')); }).catch(function(e) { self.fail(e); });
				} }, [ _('Save') ]) ]) ]);
		name.focus();
	},

	topup: function(p) {
		var self = this, last = (p.history || [])[0];
		var size = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 0.1, 'step': 0.1, 'value': last ? last.size_gb : gb(p.size), 'style': 'width:8em' });
		var leftover = Math.max(0, p.left);
		var fresh = E('input', { 'type': 'radio', 'name': 'ws-topup', 'value': 'fresh', 'checked': '' });
		var add = E('input', { 'type': 'radio', 'name': 'ws-topup', 'value': 'add' });
		ui.showModal(_('Bought more for %s').format(p.name), [
			E('div', { 'class': 'ws' }, [
				E('div', { 'class': 'ws-plot-label' }, [ _('How much did you buy (GB)?') ]), size,
				leftover > 1e8 ? E('div', { 'style': 'margin-top:12px' }, [
					E('div', {}, [ E('label', {}, [ fresh, ' ', _('Start fresh: the %s left are gone').format(ws.bytes(leftover)) ]) ]),
					E('div', {}, [ E('label', {}, [ add, ' ', _('Add it to the %s left').format(ws.bytes(leftover)) ]) ]) ]) : '',
				E('p', { 'class': 'ws-note', 'style': 'margin-top:10px' }, [ _('The count starts again from now. The bundle you had is kept in the history below the plan.') ]) ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					ui.hideModal();
					ws.topup(p.id, +size.value || 0, add.checked ? 'add' : 'fresh').then(function() { return self.reload(_('Done: the count starts again.')); }).catch(function(e) { self.fail(e); });
				} }, [ _('Save') ]) ]) ]);
		size.focus();
	},

	calibrate: function(p) {
		var self = this;
		var left = E('input', { 'class': 'cbi-input-text', 'type': 'number', 'min': 0, 'step': 0.01, 'value': gb(Math.max(0, p.left)), 'style': 'width:8em' });
		ui.showModal(_('Correct %s').format(p.name), [
			E('div', { 'class': 'ws' }, [
				E('div', { 'class': 'ws-plot-label' }, [ _('How much does the carrier say is left (GB)?') ]), left,
				E('p', { 'class': 'ws-note', 'style': 'margin-top:10px' }, [ _('wanscope counts on the router\'s links; the carrier also counts the modem\'s own traffic and rounds its own way. This makes the two match from now on.') ]) ]),
			E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Cancel') ]), ' ',
				E('button', { 'class': 'btn cbi-button-action', 'click': function() {
					ui.hideModal();
					ws.calibrate(p.id, +left.value || 0).then(function() { return self.reload(_('Corrected.')); }).catch(function(e) { self.fail(e); });
				} }, [ _('Save') ]) ]) ]);
		left.focus();
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
