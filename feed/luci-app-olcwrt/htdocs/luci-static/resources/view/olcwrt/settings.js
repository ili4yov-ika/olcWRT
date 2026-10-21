'use strict';
'require view';
'require form';
'require uci';

return view.extend({
	load() {
		return uci.load('olcwrt');
	},

	render() {
		const m = new form.Map('olcwrt', _('Settings'), _('General settings of the olcRTC client.'));

		let s = m.section(form.NamedSection, 'main', 'main');
		s.tab('general', _('General'));
		s.tab('proxy', _('SOCKS5 proxy'));
		s.tab('advanced', _('Advanced'));

		let o = s.taboption('general', form.Flag, 'enabled', _('Enable'),
			_('Start olcWRT now and at boot.'));
		o.rmempty = false;

		o = s.taboption('general', form.ListValue, 'log_level', _('Log level'),
			_('Debug also logs every proxied connection, which uses more memory.'));
		o.value('info', _('Normal'));
		o.value('debug', _('Debug'));
		o.default = 'info';

		o = s.taboption('general', form.Value, 'dns_server', _('DNS for olcrtc'),
			_('Resolver olcrtc uses to reach the video service, host:port. "auto" uses the DNS servers of the WAN connection. It must not point at the router itself.'));
		o.value('auto', _('auto (WAN DNS)'));
		o.value('77.88.8.8:53', '77.88.8.8:53 (Yandex)');
		o.value('8.8.8.8:53', '8.8.8.8:53 (Google)');
		o.value('1.1.1.1:53', '1.1.1.1:53 (Cloudflare)');
		o.default = 'auto';
		o.validate = function(sid, v) {
			if (!v || v == 'auto')
				return true;
			if (/^(127\.|localhost|::1|\[::1\])/.test(v))
				return _('The router itself cannot be used: its DNS may depend on the tunnel');
			return /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+)(:\d{1,5})?$/.test(v) ? true : _('Expecting host or host:port');
		};

		o = s.taboption('general', form.Value, 'failover_retry', _('Failover delay'),
			_('Pause before trying the next server, e.g. 2s.'));
		o.placeholder = '2s';

		o = s.taboption('general', form.Value, 'check_url', _('Check URL'),
			_('Requested through the tunnel by "Check connection".'));
		o.placeholder = 'http://cp.cloudflare.com/generate_204';

		o = s.taboption('proxy', form.Value, 'socks_port', _('SOCKS5 port'),
			_('Port of the olcrtc SOCKS5 proxy on the router.'));
		o.datatype = 'port';
		o.placeholder = '8808';

		o = s.taboption('proxy', form.Flag, 'lan_socks', _('Allow access from the network'),
			_('Listen on all interfaces so devices can use the proxy directly. A login and password are required.'));

		o = s.taboption('proxy', form.Value, 'socks_user', _('Username'));
		o.depends('lan_socks', '1');
		o.rmempty = false;

		o = s.taboption('proxy', form.Value, 'socks_pass', _('Password'));
		o.password = true;
		o.depends('lan_socks', '1');
		o.rmempty = false;

		o = s.taboption('advanced', form.Value, 'redir_port', _('Transparent proxy port'));
		o.datatype = 'port';
		o.placeholder = '18809';

		o = s.taboption('advanced', form.Value, 'dns_port', _('DNS resolver port'),
			_('Local port of the olcWRT resolver used by dnsmasq.'));
		o.datatype = 'port';
		o.placeholder = '18853';

		o = s.taboption('advanced', form.Value, 'mem_limit', _('olcrtc memory limit'),
			_('Soft limit for the Go runtime of olcrtc, e.g. 64M. Helps on routers with little RAM. Empty: no limit.'));
		o.placeholder = '64M';
		o.validate = (sid, v) => (!v || /^\d+\s*([kmg](i?b)?|b)?$/i.test(v)) ? true : _('Expecting a size such as 64M');

		return m.render();
	}
});
