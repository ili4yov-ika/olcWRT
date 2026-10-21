'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require dom';
'require olcwrt.common as olc';

function metaSummary(meta) {
	if (!meta || (!meta.last_update && !meta.last_error))
		return E('em', {}, _('not downloaded yet'));
	const f = meta.fields || {};
	const parts = [ _('%d servers').format(meta.count || 0) ];
	if (f.used)
		parts.push(_('used %s').format(f.used));
	if (f.available)
		parts.push(_('available %s').format(f.available));
	if (f.expire && +f.expire > 0)
		parts.push(_('expires %s').format(olc.formatTime(+f.expire)));
	const nodes = [ E('div', {}, parts.join(', ')), E('small', {}, _('updated %s').format(olc.formatTime(meta.last_update))) ];
	if (meta.last_error)
		nodes.push(E('div', { 'style': 'color:#c62828' }, meta.last_error));
	return E('div', {}, nodes);
}

return view.extend({
	load() {
		return Promise.all([ uci.load('olcwrt'), olc.status() ]);
	},

	render(data) {
		const subsMeta = (data[1] || {}).subscriptions || {};

		const m = new form.Map('olcwrt', _('Subscriptions'),
			_('A subscription is a URL with a list of olcrtc:// servers. Its servers are added to the server list and kept up to date automatically.'));

		const s = m.section(form.GridSection, 'subscription');
		s.addremove = true;
		s.anonymous = true;
		s.nodescriptions = true;
		s.addbtntitle = _('Add subscription');
		s.modaltitle = sid => _('Subscription') + ' » ' + (uci.get('olcwrt', sid, 'label') || uci.get('olcwrt', sid, 'url') || sid);

		/* Servers of a removed subscription are removed with it. */
		s.handleRemove = function(section_id, ev) {
			const selected = L.toArray(uci.get('olcwrt', 'main', 'server'));
			const gone = [];
			uci.sections('olcwrt', 'server').forEach(srv => {
				if (srv.subscription == section_id) {
					gone.push(srv['.name']);
					uci.remove('olcwrt', srv['.name']);
				}
			});
			const keep = selected.filter(id => gone.indexOf(id) < 0);
			if (keep.length != selected.length)
				uci.set('olcwrt', 'main', 'server', keep.length ? keep : null);
			return this.super('handleRemove', [ section_id, ev ]);
		};

		s.renderRowActions = function(section_id) {
			const td = this.super('renderRowActions', [ section_id, _('Edit') ]);
			td.lastElementChild.insertBefore(E('button', {
				'class': 'btn cbi-button cbi-button-action',
				'click': ui.createHandlerFn(this, () => updateSubs(section_id))
			}, _('Update')), td.lastElementChild.firstChild);
			return td;
		};

		let o = s.option(form.Value, 'label', _('Name'));
		o.rmempty = true;

		o = s.option(form.Value, 'url', _('URL'));
		o.rmempty = false;
		o.validate = function(sid, v) {
			return /^https?:\/\/\S+$/i.test(v || '') ? true : _('Expecting an http:// or https:// URL');
		};
		o.textvalue = function(sid) {
			const v = this.cfgvalue(sid) || '';
			return v.length > 48 ? v.substr(0, 45) + '…' : v;
		};

		o = s.option(form.Flag, 'enabled', _('Auto update'));
		o.default = '1';
		o.editable = true;

		o = s.option(form.Value, 'interval', _('Update interval'),
			_('For example 30m, 6h, 1d. Empty: as announced by the subscription, otherwise daily. 0: manual updates only.'));
		o.modalonly = true;
		o.placeholder = _('automatic');
		o.validate = function(sid, v) {
			return (!v || /^\d+[smhd]?$/.test(v)) ? true : _('Expecting a duration such as 30m, 6h or 1d');
		};

		o = s.option(form.Flag, 'via_tunnel', _('Download through the tunnel'),
			_('Try the tunnel first; by default the subscription is downloaded directly and the tunnel is only a fallback.'));
		o.modalonly = true;

		o = s.option(form.ListValue, 'request_mode', _('Request identity'),
			_('"olcWRT" sends the User-Agent olcwrt/version and a persistent x-hwid header, like olcbox. "Browser" pretends to be a web browser.'));
		o.modalonly = true;
		o.value('identity', 'olcWRT');
		o.value('browser', _('Browser'));

		o = s.option(form.DummyValue, '_info', _('State'));
		o.modalonly = false;
		o.textvalue = sid => metaSummary(subsMeta[sid]);
		o.cfgvalue = () => null;
		o.renderWidget = (sid) => metaSummary(subsMeta[sid]);

		return m.render().then(node => E([], [
			E('div', { 'class': 'right' }, [
				E('button', {
					'class': 'btn cbi-button cbi-button-action',
					'click': ui.createHandlerFn(this, () => updateSubs(''))
				}, _('Update all now'))
			]),
			node
		]));
	}
});

async function updateSubs(id) {
	try {
		const [ ok, text ] = olc.subResultsSummary(await olc.runJob('sub_update', id, 300));
		ui.addNotification(null, E('p', text), ok ? 'info' : 'warning');
		window.setTimeout(() => location.reload(), 1500);
	}
	catch (err) {
		olc.notifyError(err);
	}
}
