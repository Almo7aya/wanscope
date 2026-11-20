'use strict';
'require view';
'require poll';
'require ui';
'require wanscope.ui as ws';

// Network > wanscope > Blocked: every blocked service, where it is blocked, how many of its servers' addresses
// are blocked and what was dropped, with Unblock. Services are blocked from the Activity page.

return view.extend({
	load: function() {
		return ws.blocks();
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.rows = (res || {}).blocks || [];
		this.box = E('div');
		var root = E('div', { 'class': 'ws' }, [
			E('h2', {}, [ _('Blocked services') ]),
			E('p', { 'class': 'ws-sub' }, [ _('Services blocked by IP: traffic to their servers\' addresses is dropped, on one device or on every device. Block one with the Block button on the '),
				E('a', { 'href': L.url('admin/network/wanscope/activity') }, [ _('Activity') ]), _(' page.') ]),
			E('div', { 'class': 'ws-card' }, [ this.box ])
		]);
		this.draw();
		poll.add(function() {
			return ws.blocks().then(function(r) { self.rows = (r || {}).blocks || self.rows; self.draw(); });
		}, 5);
		return root;
	},

	draw: function() {
		var self = this;
		if (!this.rows.length) {
			this.box.replaceChildren(E('div', { 'class': 'ws-empty' }, [ _('Nothing is blocked. Block a service with the Block button on the Activity page.') ]));
			return;
		}
		this.box.replaceChildren(E('div', { 'class': 'ws-scroll' }, [ E('table', { 'class': 'ws-t' }, [
			E('tr', {}, [ E('th', {}, [ _('Service') ]), E('th', {}, [ _('Blocked on') ]), E('th', { 'class': 'r' }, [ _('Addresses blocked') ]),
				E('th', { 'class': 'r' }, [ _('Dropped') ]), E('th', {}, [ '' ]) ])
		].concat(this.rows.map(function(b) {
			return E('tr', {}, [
				E('td', {}, [ E('b', {}, [ b.service ]) ]),
				E('td', {}, [ b.device
					? E('a', { 'href': L.url('admin/network/wanscope/devices') + '#' + encodeURIComponent(b.device) }, [ b.device_name || b.device ])
					: E('span', { 'class': 'ws-chip bad' }, [ _('every device') ]) ]),
				E('td', { 'class': 'r' }, [ String(b.addresses), E('div', {}, [ E('small', {}, [ b.addresses ? _('more join as it uses them') : _('added as it shows up') ]) ]) ]),
				E('td', { 'class': 'r' }, b.packets ? [ ws.bytes(b.bytes), E('div', {}, [ E('small', {}, [ _('%d packets').format(b.packets) ]) ]) ] : [ E('small', {}, [ _('nothing yet') ]) ]),
				E('td', { 'class': 'r' }, [ E('button', { 'class': 'ws-btn', 'type': 'button', 'click': function() { self.unblock(b); } }, [ _('Unblock') ]) ])
			]);
		}))) ]));
	},

	unblock: function(b) {
		var self = this;
		ws.blockService(b.service, b.device, false).then(function() {
			ui.addNotification(null, E('p', {}, [ b.device ? _('%s is unblocked on %s.').format(b.service, b.device_name || b.device) : _('%s is unblocked on every device.').format(b.service) ]), 'info');
			return ws.blocks();
		}).then(function(r) {
			self.rows = (r || {}).blocks || [];
			self.draw();
		}).catch(function(err) { ui.addNotification(null, E('p', {}, [ err.message ]), 'danger'); });
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
