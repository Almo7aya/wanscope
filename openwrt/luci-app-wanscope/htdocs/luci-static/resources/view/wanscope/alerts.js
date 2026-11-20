'use strict';
'require view';
'require poll';
'require ui';
'require wanscope.ui as ws';

// Network > wanscope > Alerts: what wanscope noticed (new devices, caps, quotas, bursts, daily reports).

var KINDS = { new_device: _('New device'), cap: _('Data cap'), quota: _('Quota'), burst: _('Burst'), report: _('Daily report'), wan_down: _('Uplink'), test: _('Test') };

return view.extend({
	load: function() {
		return Promise.all([ ws.status(), ws.alerts(300) ]);
	},

	render: function(res) {
		ws.css();
		var self = this;
		this.st = res[0] || {};
		this.list = res[1].alerts || [];
		this.box = E('div');
		this.result = E('span', { 'class': 'ws-note' });
		var method = { none: _('off'), telegram: _('Telegram'), command: _('a command') }[this.st.notify] || this.st.notify;
		var root = E('div', { 'class': 'ws' }, [
			E('h2', {}, [ _('Alerts') ]),
			E('p', { 'class': 'ws-sub' }, [ _('What wanscope noticed. Messages are sent by: %s.').format(method), ' ',
				E('a', { 'href': L.url('admin/network/wanscope/settings') }, [ _('Change in Settings') ]) ]),
			E('div', { 'class': 'ws-card' }, [
				E('div', { 'class': 'ws-bar' }, [
					E('button', { 'class': 'ws-btn', 'click': function(ev) { self.test(ev.target); } }, [ _('Send a test message') ]), this.result,
					E('span', { 'class': 'grow' }),
					E('button', { 'class': 'ws-btn', 'click': function(ev) { self.report(ev.target); } }, [ _('Show today\'s report') ]) ]),
				this.box ])
		]);
		this.draw();
		poll.add(function() { return ws.alerts(300).then(function(r) { self.list = r.alerts || []; self.draw(); }); }, 2);
		return root;
	},

	draw: function() {
		this.box.replaceChildren(this.list.length ? E('table', { 'class': 'ws-t' }, [
			E('tr', {}, [ E('th', {}, [ _('When') ]), E('th', {}, [ _('What') ]), E('th', {}, [ _('Message') ]), E('th', {}, [ _('Sent') ]) ])
		].concat(this.list.map(function(a) {
			return E('tr', {}, [
				E('td', { 'style': 'white-space:nowrap' }, [ new Date(a.t * 1000).toLocaleString(), E('br'), E('small', {}, [ ws.ago(a.t) ]) ]),
				E('td', {}, [ KINDS[a.kind] || a.kind ]),
				E('td', { 'style': 'white-space:pre-line' }, [ a.text ]),
				E('td', {}, [ a.sent ? E('span', { 'class': 'ws-chip ' + (a.sent == 'sent' ? 'good' : 'bad') }, [ a.sent == 'sent' ? _('sent') : a.sent ]) : E('small', {}, [ _('log only') ]) ])
			]);
		}))) : E('div', { 'class': 'ws-empty' }, [ _('No alerts yet. Turn on notifications in Settings to get them on your phone.') ]));
	},

	test: function(btn) {
		var self = this;
		btn.disabled = true;
		ws.notifyTest().then(function(r) {
			self.result.replaceChildren(r.result == 'sent' ? _('Sent.') : r.result ? r.result : _('Notifications are off.'));
			return ws.alerts(300);
		}).then(function(r) { self.list = r.alerts || []; self.draw(); }).finally(function() { btn.disabled = false; });
	},

	report: function(btn) {
		btn.disabled = true;
		var d = new Date(); d.setHours(0, 0, 0, 0);
		ws.report(Math.floor(d / 1000), Math.floor(Date.now() / 1000) + 1).then(function(r) {
			ui.showModal(_('Today so far'), [ E('pre', { 'style': 'white-space:pre-wrap;font:inherit' }, [ r.text ]),
				E('div', { 'class': 'right' }, [ E('button', { 'class': 'btn', 'click': ui.hideModal }, [ _('Close') ]) ]) ]);
		}).finally(function() { btn.disabled = false; });
	},

	handleSave: null,
	handleSaveApply: null,
	handleReset: null
});
