'use strict';
'require view';
'require form';
'require uci';
'require wanscope.ui as ws';

// Network > wanscope > Settings: collection, history, notifications and custom service rules (UCI: wanscope).
// Saving restarts the daemon (procd reload trigger). Uplink names, caps and cycles are set on the Uplinks page,
// device names and limits on the Devices page.

return view.extend({
	load: function() {
		return Promise.all([ uci.load('wanscope'), ws.status() ]);
	},

	render: function(res) {
		var st = res[1] || {}, m, s, o;
		m = new form.Map('wanscope', _('wanscope settings'),
			_('How wanscope collects and keeps traffic, and how it tells you about it. Saving restarts it (a second of live data is lost).'));

		s = m.section(form.NamedSection, 'main', 'wanscope', _('Collection'));
		s.addremove = false;
		o = s.option(form.Flag, 'enabled', _('Enabled'));
		o.default = '1';
		o = s.option(form.Flag, 'sniff', _('Name connections'),
			_('Read DNS answers and the server names in TLS and QUIC handshakes on the LAN to tell YouTube from Steam. Only those packets are read, nothing is stored but the names.'));
		o.default = '1';
		o = s.option(form.ListValue, 'interval', _('Sampling'), _('How often conntrack is read. Every second gives the live view; slower routers can use 2 s.'));
		o.value('1', _('every second'));
		o.value('2', _('every 2 seconds'));
		o.value('5', _('every 5 seconds'));
		o.default = '1';
		o = s.option(form.Value, 'offline_timeout', _('Offline after (s)'), _('A device without traffic for this long shows as offline.'));
		o.datatype = 'range(30,3600)';
		o.default = '180';

		s = m.section(form.NamedSection, 'main', 'wanscope', _('History'));
		o = s.option(form.Value, 'data_dir', _('Data folder'), _('Where history files go. On routers with little flash, point this at a USB disk. Disk use now: %s.').format(ws.bytes(st.disk || 0)));
		o.default = '/usr/share/wanscope';
		o = s.option(form.Value, 'live_seconds', _('Live window (s)'), _('Seconds of 1-second points kept in memory.'));
		o.datatype = 'range(60,86400)';
		o.default = '3600';
		o = s.option(form.Value, 'minute_days', _('Keep minutes (days)'), _('Minute detail kept on disk; a busy home uses about 0.5-1 MB a day.'));
		o.datatype = 'range(1,3650)';
		o.default = '365';
		o = s.option(form.Value, 'hour_days', _('Keep hours (days)'), _('Daily totals are kept for good.'));
		o.datatype = 'range(7,36500)';
		o.default = '731';
		o = s.option(form.Value, 'memory_days', _('Days of hours in memory'), _('Quotas and caps read these. More days use more memory.'));
		o.datatype = 'range(2,400)';
		o.default = '35';

		s = m.section(form.NamedSection, 'guest', 'guest', _('New devices'),
			_('What a device gets when it joins for the first time, until you approve it on its page. Turning this on approves every device known now.'));
		s.addremove = false;
		o = s.option(form.ListValue, 'policy', _('New devices get'));
		o.value('allow', _('internet right away'));
		o.value('limit', _('guest speed until approved'));
		o.value('block', _('no internet until approved'));
		o.default = 'allow';
		o = s.option(form.Value, 'down', _('Guest download (kB/s)'), _('1000 kB/s = 1 MB/s; 0 for no limit.'));
		o.datatype = 'uinteger';
		o.depends('policy', 'limit');
		o = s.option(form.Value, 'up', _('Guest upload (kB/s)'));
		o.datatype = 'uinteger';
		o.depends('policy', 'limit');
		o = s.option(form.Value, 'since', _('Approval started'));
		o.readonly = true;
		o.modalonly = false;
		o.depends('policy', 'limit');
		o.depends('policy', 'block');
		o.cfgvalue = function(sid) { var v = uci.get('wanscope', sid, 'since'); return v && +v ? new Date(+v * 1000).toLocaleString() : _('when you save'); };
		o.write = function() {};

		s = m.section(form.NamedSection, 'notify', 'notify', _('Notifications'));
		s.addremove = false;
		o = s.option(form.ListValue, 'method', _('Send alerts by'));
		o.value('none', _('nothing (log only)'));
		o.value('telegram', _('Telegram bot'));
		o.value('command', _('a command'));
		o.default = 'none';
		o = s.option(form.Value, 'telegram_token', _('Bot token'), _('From @BotFather.'));
		o.password = true;
		o.depends('method', 'telegram');
		o = s.option(form.Value, 'telegram_chat', _('Chat id'), _('Your user, group or channel id.'));
		o.depends('method', 'telegram');
		o = s.option(form.Value, 'command', _('Command'), _('Run with the message as its last argument, e.g. /usr/libexec/netmode/notify to use netmode\'s bot.'));
		o.depends('method', 'command');
		o = s.option(form.Flag, 'new_device', _('New devices'), _('When a device joins for the first time.'));
		o.default = '1';
		o = s.option(form.Flag, 'quota', _('Quotas'), _('When a device uses up a quota set on the Devices page.'));
		o.default = '1';
		o = s.option(form.DynamicList, 'cap_percent', _('Data plan alerts (%)'), _('Alert when a data plan (set on the Uplinks page) has used these shares of its bundle.'));
		o.datatype = 'range(1,500)';
		o = s.option(form.Flag, 'wan_offline', _('Uplink offline'), _('When an uplink goes down or comes back.'));
		o = s.option(form.Value, 'burst_mb', _('Burst alert (MB)'), _('Alert when one device uses this much within the window below; 0 is off.'));
		o.datatype = 'ufloat';
		o.default = '0';
		o = s.option(form.Value, 'burst_minutes', _('Burst window (min)'));
		o.datatype = 'range(1,1440)';
		o.default = '10';
		o = s.option(form.Flag, 'daily_report', _('Daily report'), _('Yesterday per uplink and the busiest devices.'));
		o = s.option(form.Value, 'report_time', _('Report time'));
		o.placeholder = '09:00';
		o.depends('daily_report', '1');
		o.validate = function(id, v) { return !v || /^([01]?\d|2[0-3]):[0-5]\d$/.test(v) ? true : _('Use HH:MM'); };

		s = m.section(form.GridSection, 'rule', _('Your own services'),
			_('Name traffic the built-in list doesn\'t know: by domain (and its subdomains), address range or port. Your rules win over the built-in ones.'));
		s.addremove = true;
		s.anonymous = true;
		s.sortable = true;
		o = s.option(form.Value, 'name', _('Service'));
		o.rmempty = false;
		o = s.option(form.ListValue, 'category', _('Kind'));
		[ [ 'video', _('video') ], [ 'games', _('games') ], [ 'social', _('social') ], [ 'messaging', _('messaging') ], [ 'music', _('music') ],
			[ 'cloud', _('cloud') ], [ 'updates', _('updates') ], [ 'work', _('work') ], [ 'other', _('other') ] ].forEach(function(c) { o.value(c[0], c[1]); });
		o.default = 'other';
		o = s.option(form.DynamicList, 'domain', _('Domains'));
		o.placeholder = 'example.com';
		o = s.option(form.DynamicList, 'cidr', _('Addresses'));
		o.datatype = 'or(cidr,ipaddr)';
		o = s.option(form.DynamicList, 'port', _('Ports'), _('tcp:25565 or udp:7000-7999'));
		o.validate = function(id, v) { return !v || /^(tcp|udp):\d+(-\d+)?$/.test(v) ? true : _('tcp:PORT or udp:FROM-TO'); };

		return m.render();
	}
});
