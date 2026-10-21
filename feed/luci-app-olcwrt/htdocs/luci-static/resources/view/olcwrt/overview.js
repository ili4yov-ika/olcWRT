'use strict';
'require view';
'require poll';
'require uci';
'require ui';
'require dom';
'require olcwrt.common as olc';

const modeNames = {
	all: _('All traffic through the tunnel'),
	list: _('Only listed destinations'),
	off: _('Off (SOCKS5 proxy only)')
};

const dnsNames = {
	tunnel: _('All queries through the tunnel'),
	lists: _('Listed domains only'),
	off: _('Not integrated')
};

function row(title, value) {
	return E('tr', { 'class': 'tr' }, [
		E('td', { 'class': 'td left', 'width': '33%' }, title),
		E('td', { 'class': 'td left' }, value)
	]);
}

function serverLabel(id) {
	const s = uci.get('olcwrt', id);
	if (!s)
		return id;
	return '%s (%s, %s)'.format(s.label || s.room || id, olc.providerName(s.provider), olc.transportName(s.transport));
}

return view.extend({
	load() {
		return Promise.all([ uci.load('olcwrt'), olc.status() ]);
	},

	render(data) {
		const status = data[1];
		this.warnNode = E('div');
		this.statusTable = E('table', { 'class': 'table' });
		this.statsTable = E('table', { 'class': 'table' });
		this.checkNode = E('span', { 'style': 'margin-left:1em' });

		const view = E([], [
			E('h2', _('olcWRT')),
			E('div', { 'class': 'cbi-map-descr' },
				_('olcRTC client: an encrypted TCP tunnel disguised as a video call on Jitsi, Yandex Telemost or WB Stream.')),
			this.warnNode,
			E('div', { 'class': 'cbi-section' }, [ E('h3', _('Status')), this.statusTable ]),
			E('div', { 'class': 'cbi-section' }, [ E('h3', _('Control')), this.renderControls() ]),
			E('div', { 'class': 'cbi-section' }, [ E('h3', _('Statistics')), this.statsTable ])
		]);

		this.update(status);
		poll.add(() => olc.status().then(st => this.update(st)), 3);
		return view;
	},

	renderControls() {
		const enabled = uci.get('olcwrt', 'main', 'enabled') == '1';
		const servers = uci.sections('olcwrt', 'server');
		const selected = L.toArray(uci.get('olcwrt', 'main', 'server'))[0] || '';

		const select = E('select', { 'class': 'cbi-input-select', 'style': 'min-width:20em' },
			servers.map(s => E('option', {
				'value': s['.name'],
				'selected': s['.name'] == selected ? '' : null
			}, serverLabel(s['.name']))));

		const buttons = [];
		if (enabled) {
			buttons.push(
				E('button', { 'class': 'btn cbi-button cbi-button-reload', 'click': ui.createHandlerFn(this, 'handleRestart') }, _('Restart')), ' ',
				E('button', { 'class': 'btn cbi-button cbi-button-reset', 'click': ui.createHandlerFn(this, 'handleEnable', false) }, _('Disable')), ' ');
		}
		else {
			buttons.push(
				E('button', {
					'class': 'btn cbi-button cbi-button-apply',
					'click': ui.createHandlerFn(this, 'handleEnable', true),
					'disabled': servers.length ? null : ''
				}, _('Enable and start')), ' ');
		}
		buttons.push(
			E('button', { 'class': 'btn cbi-button cbi-button-action', 'click': ui.createHandlerFn(this, 'handleCheck') }, _('Check connection')),
			this.checkNode);

		return E('div', {}, [
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Service')),
				E('div', { 'class': 'cbi-value-field' }, buttons)
			]),
			E('div', { 'class': 'cbi-value' }, [
				E('label', { 'class': 'cbi-value-title' }, _('Server')),
				E('div', { 'class': 'cbi-value-field' }, servers.length ? [
					select, ' ',
					E('button', {
						'class': 'btn cbi-button cbi-button-apply',
						'click': ui.createHandlerFn(this, () => this.handleSwitch(select.value))
					}, _('Use this server'))
				] : [ E('em', {}, _('No servers yet: add them on the Servers or Subscriptions page.')) ])
			])
		]);
	},

	update(st) {
		const d = st.daemon || {};
		const running = !!st.running;
		const state = running ? (d.state || 'starting') : 'stopped';
		const now = Date.now() / 1000;

		const warnings = [];
		if (st.olcrtc_present === false)
			warnings.push(_('The olcrtc binary is missing: install the olcrtc package.'));
		if (st.stale)
			warnings.push(_('The daemon does not respond.'));
		if (running)
			(d.warnings || []).forEach(w => warnings.push(w));
		dom.content(this.warnNode, warnings.map(w => E('div', { 'class': 'alert-message warning' }, w)));

		const tunnel = [ olc.stateBadge(state) ];
		if (running && d.since)
			tunnel.push(' ', E('small', {}, _('for %s').format(olc.formatDuration(now - d.since))));

		const rows = [
			row(_('Service'), running ? _('Running (PID %d)').format(d.pid || 0) : _('Not running')),
			row(_('Tunnel'), tunnel)
		];
		if (running && d.server)
			rows.push(row(_('Active server'), serverLabel(d.server)));
		if (running && (d.chain || []).length > 1)
			rows.push(row(_('Failover order'), d.chain.map(serverLabel).join(' → ')));
		if (running && d.mode) {
			rows.push(row(_('Routing'), '%s%s'.format(modeNames[d.mode] || d.mode,
				(d.devices || []).length ? ' (' + d.devices.join(', ') + ')' : '')));
			rows.push(row(_('DNS'), dnsNames[d.dns_mode] || dnsNames.off));
		}
		if (running && d.socks)
			rows.push(row(_('SOCKS5 proxy'), d.socks));
		if (running && d.restarts)
			rows.push(row(_('olcrtc restarts'), String(d.restarts)));
		if (d.last_error && state != 'connected')
			rows.push(row(_('Last error'), E('span', { 'style': 'color:#c62828' }, d.last_error)));
		dom.content(this.statusTable, rows);

		const r = d.redir || {}, q = d.resolver || {}, l = d.lists || {};
		dom.content(this.statsTable, running ? [
			row(_('Connections'), _('%d active, %d total, %d failed').format(r.active || 0, r.total || 0, r.failed || 0)),
			row(_('Traffic'), _('↑ %s ↓ %s').format(olc.formatBytes(r.bytes_up), olc.formatBytes(r.bytes_down))),
			row(_('DNS queries'), _('%d total, %d through the tunnel, %d direct, %d failed').format(
				q.queries || 0, q.tunnel || 0, q.direct || 0, q.errors || 0)),
			row(_('Learned addresses'), String(q.learned || 0)),
			row(_('Lists'), _('proxy: %d domains, %d networks; bypass: %d domains, %d networks').format(
				l.proxy_domains || 0, l.proxy_ips || 0, l.bypass_domains || 0, l.bypass_ips || 0))
		] : [ row(_('Statistics'), _('The service is not running')) ]);
	},

	async applyMainChange(fn) {
		fn();
		await uci.save();
		ui.changes.apply(true);
	},

	handleEnable(enable) {
		return this.applyMainChange(() => uci.set('olcwrt', 'main', 'enabled', enable ? '1' : '0'));
	},

	handleSwitch(id) {
		if (!id)
			return;
		return this.applyMainChange(() => uci.set('olcwrt', 'main', 'server', [ id ]));
	},

	handleRestart() {
		return olc.initAction('restart').then(() => {
			ui.addNotification(null, E('p', _('olcWRT is restarting')), 'info');
		}).catch(olc.notifyError);
	},

	handleCheck() {
		dom.content(this.checkNode, E('em', { 'class': 'spinning' }, _('Checking…')));
		return olc.runJob('check', '', 120).then(res => {
			dom.content(this.checkNode, E('span', {
				'style': res && res.ok ? 'color:#2e7d32' : 'color:#c62828'
			}, olc.checkSummary(res)));
		}).catch(err => {
			dom.content(this.checkNode, E('span', { 'style': 'color:#c62828' }, err.message));
		});
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
