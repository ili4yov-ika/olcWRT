'use strict';
'require view';
'require form';
'require fs';
'require uci';
'require ui';
'require network';
'require tools.widgets as widgets';
'require olcwrt.common as olc';

const listFiles = {
	proxy: '/etc/olcwrt/proxy.lst',
	bypass: '/etc/olcwrt/bypass.lst'
};

function normalizeList(text) {
	return (text || '').replace(/\r\n?/g, '\n').trim() + '\n';
}

/* A textarea option backed by a list file instead of UCI. */
function listEditor(s, kind, title, description) {
	const o = s.taboption('lists', form.TextValue, '_' + kind + '_list', title, description);
	o.rows = 14;
	o.monospace = true;
	o.wrap = 'off';
	o.load = () => L.resolveDefault(fs.read(listFiles[kind]), '');
	o.write = (sid, value) => fs.write(listFiles[kind], normalizeList(value)).then(() => olc.reload());
	o.remove = () => fs.write(listFiles[kind], '').then(() => olc.reload());
	return o;
}

function listURLs(s, kind, title) {
	const o = s.taboption('lists', form.DynamicList, kind + '_list_url', title,
		_('Plain text lists downloaded daily: domains, IP addresses or subnets, one per line. dnsmasq server=/nftset= lines are understood as well.'));
	o.datatype = 'string';
	o.placeholder = 'https://example.org/list.txt';
	o.validate = (sid, v) => (!v || /^https?:\/\/\S+$/i.test(v)) ? true : _('Expecting an http:// or https:// URL');
	return o;
}

return view.extend({
	load() {
		return Promise.all([
			uci.load('olcwrt'),
			L.resolveDefault(network.getHostHints(), null),
			olc.status()
		]);
	},

	render(data) {
		const hosts = data[1];
		const lists = (data[2] || {}).lists || {};

		const m = new form.Map('olcwrt', _('Routing'),
			_('Which traffic of the local network goes through the tunnel. olcRTC carries TCP only: UDP (except DNS) always goes directly.'));

		let s = m.section(form.NamedSection, 'routing', 'routing', _('Traffic'));
		s.tab('general', _('General'));
		s.tab('clients', _('Clients'));
		s.tab('lists', _('Lists'));

		let o = s.taboption('general', form.ListValue, 'mode', _('Mode'));
		o.value('all', _('All traffic through the tunnel, except the bypass list'));
		o.value('list', _('Only destinations from the proxy list'));
		o.value('off', _('Off: no redirection, SOCKS5 proxy only'));
		o.default = 'all';

		o = s.taboption('general', widgets.NetworkSelect, 'interface', _('Interfaces'),
			_('Networks whose clients are redirected.'));
		o.multiple = true;
		o.nocreate = true;
		o.default = 'lan';

		o = s.taboption('general', form.Flag, 'ipv6', _('Redirect IPv6'),
			_('Also send IPv6 TCP traffic through the tunnel. Disable if the olcrtc server has no IPv6.'));
		o.default = '1';

		o = s.taboption('general', form.Flag, 'block_quic', _('Block QUIC'),
			_('Reject UDP port 443 to proxied destinations so browsers fall back to TCP, which the tunnel can carry.'));
		o.default = '1';

		o = s.taboption('general', form.Flag, 'dns_hijack', _('Intercept DNS'),
			_('Redirect DNS queries of clients with their own DNS servers to the router.'));
		o.default = '1';

		o = s.taboption('clients', form.ListValue, 'source_mode', _('Clients'));
		o.value('all', _('All clients'));
		o.value('include', _('Only the listed clients'));
		o.value('exclude', _('All clients except the listed ones'));
		o.default = 'all';

		o = s.taboption('clients', form.DynamicList, 'source', _('Client list'),
			_('MAC addresses, IP addresses or subnets.'));
		o.datatype = 'or(macaddr,cidr,ipaddr)';
		o.depends('source_mode', 'include');
		o.depends('source_mode', 'exclude');
		if (hosts)
			hosts.getMACHints(true).forEach(h => o.value(h[0], h[1] ? '%s (%s)'.format(h[0], h[1]) : h[0]));

		listEditor(s, 'proxy', _('Proxy list'),
			_('Domains (with subdomains), addresses and subnets that always go through the tunnel. In "only listed" mode nothing else does.'));
		listURLs(s, 'proxy', _('Proxy list URLs'));
		listEditor(s, 'bypass', _('Bypass list'),
			_('Domains, addresses and subnets that never go through the tunnel. Private networks are always bypassed.'));
		listURLs(s, 'bypass', _('Bypass list URLs'));

		o = s.taboption('lists', form.Button, '_update_lists', _('Downloaded lists'));
		o.inputtitle = _('Update now');
		o.inputstyle = 'action';
		o.onclick = ui.createHandlerFn(this, async () => {
			try {
				const idx = await olc.runJob('lists_update', '', 300);
				const failed = Object.values(idx || {}).filter(l => l.last_error);
				ui.addNotification(null, E('p', failed.length
					? failed.map(l => '%s: %s'.format(l.url, l.last_error)).join('; ')
					: _('Lists updated')), failed.length ? 'warning' : 'info');
			}
			catch (err) {
				olc.notifyError(err);
			}
		});
		o.description = Object.keys(lists).length
			? Object.values(lists).map(l => _('%h: %d entries, updated %s').format(l.url, l.entries || 0, olc.formatTime(l.last_update))).join('<br>')
			: _('No downloaded lists.');

		s = m.section(form.NamedSection, 'dns', 'dns', _('DNS'),
			_('olcWRT plugs into dnsmasq: queries for listed domains are answered through the tunnel and their addresses are routed accordingly.'));

		o = s.option(form.Flag, 'enabled', _('Integrate with dnsmasq'),
			_('Required for domain lists.'));
		o.default = '1';

		o = s.option(form.Flag, 'tunnel', _('Resolve all names through the tunnel'),
			_('In "all traffic" mode every query except the bypass list is resolved through the tunnel. This avoids DNS spoofing and leaks.'));
		o.default = '1';
		o.depends('enabled', '1');

		o = s.option(form.DynamicList, 'upstream', _('DNS servers behind the tunnel'),
			_('Queried over TCP through the tunnel.'));
		o.placeholder = '1.1.1.1:53';
		o.depends('enabled', '1');

		o = s.option(form.DynamicList, 'direct', _('Direct DNS servers'),
			_('Used for bypassed domains and for downloads. Empty: the servers of the WAN connection.'));
		o.placeholder = _('from WAN');
		o.depends('enabled', '1');

		o = s.option(form.Flag, 'filter_aaaa', _('Drop IPv6 answers'),
			_('Answer AAAA queries resolved through the tunnel with no addresses. Useful when the olcrtc server has no IPv6.'));
		o.depends('enabled', '1');

		o = s.option(form.Value, 'set_timeout', _('Address lifetime'),
			_('How long addresses learned from DNS answers stay in the routing sets.'));
		o.placeholder = '12h';
		o.depends('enabled', '1');

		return m.render();
	}
});
