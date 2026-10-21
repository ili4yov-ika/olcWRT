'use strict';
'require view';
'require form';
'require uci';
'require ui';
'require dom';
'require olcwrt.common as olc';

function serverTitle(s) {
	return s.label || s.room || s['.name'];
}

function addTransportOptions(s) {
	let o;

	o = s.option(form.Value, 'vp8_fps', _('VP8 FPS'));
	o.modalonly = true;
	o.datatype = 'range(1,240)';
	o.placeholder = '30';
	o.depends('transport', 'vp8channel');

	o = s.option(form.Value, 'vp8_batch', _('VP8 frames per tick'));
	o.modalonly = true;
	o.datatype = 'uinteger';
	o.placeholder = '64';
	o.depends('transport', 'vp8channel');

	o = s.option(form.Value, 'sei_fps', _('SEI FPS'));
	o.modalonly = true;
	o.datatype = 'range(1,240)';
	o.placeholder = '30';
	o.depends('transport', 'seichannel');

	o = s.option(form.Value, 'sei_batch', _('SEI frames per tick'));
	o.modalonly = true;
	o.datatype = 'uinteger';
	o.placeholder = '64';
	o.depends('transport', 'seichannel');

	o = s.option(form.Value, 'sei_frag', _('SEI fragment size'), _('Bytes'));
	o.modalonly = true;
	o.datatype = 'range(1,60000)';
	o.placeholder = '900';
	o.depends('transport', 'seichannel');

	o = s.option(form.Value, 'sei_ack_ms', _('SEI ACK timeout'), _('Milliseconds'));
	o.modalonly = true;
	o.datatype = 'uinteger';
	o.placeholder = '2000';
	o.depends('transport', 'seichannel');

	o = s.option(form.ListValue, 'video_codec', _('Video codec'));
	o.modalonly = true;
	o.value('', _('Default (qrcode)'));
	o.value('qrcode', 'QR code');
	o.value('tile', _('Tile (requires 1080x1080)'));
	o.depends('transport', 'videochannel');

	o = s.option(form.Value, 'video_w', _('Video width'));
	o.modalonly = true;
	o.datatype = 'range(16,8192)';
	o.placeholder = '1920';
	o.depends('transport', 'videochannel');

	o = s.option(form.Value, 'video_h', _('Video height'));
	o.modalonly = true;
	o.datatype = 'range(16,8192)';
	o.placeholder = '1080';
	o.depends('transport', 'videochannel');

	o = s.option(form.Value, 'video_fps', _('Video FPS'));
	o.modalonly = true;
	o.datatype = 'range(1,240)';
	o.placeholder = '30';
	o.depends('transport', 'videochannel');

	o = s.option(form.ListValue, 'video_qr_recovery', _('QR error correction'));
	o.modalonly = true;
	o.value('', _('Default (low)'));
	[ 'low', 'medium', 'high', 'highest' ].forEach(v => o.value(v, v));
	o.depends('video_codec', '');
	o.depends('video_codec', 'qrcode');

	o = s.option(form.Value, 'video_qr_size', _('QR fragment size'), _('Bytes, 0 = automatic'));
	o.modalonly = true;
	o.datatype = 'uinteger';
	o.depends('video_codec', '');
	o.depends('video_codec', 'qrcode');

	o = s.option(form.Value, 'video_tile_module', _('Tile size'), _('Pixels, 1-270'));
	o.modalonly = true;
	o.datatype = 'range(1,270)';
	o.placeholder = '4';
	o.depends('video_codec', 'tile');

	o = s.option(form.Value, 'video_tile_rs', _('Reed-Solomon parity'), _('Percent, 0-200'));
	o.modalonly = true;
	o.datatype = 'range(0,200)';
	o.placeholder = '0';
	o.depends('video_codec', 'tile');
}

return view.extend({
	load() {
		return Promise.all([ uci.load('olcwrt'), olc.status() ]);
	},

	render(data) {
		const status = data[1] || {};
		const subNames = {};
		uci.sections('olcwrt', 'subscription').forEach(s => {
			const meta = (status.subscriptions || {})[s['.name']] || {};
			subNames[s['.name']] = s.label || meta.name || s.url || s['.name'];
		});

		const m = new form.Map('olcwrt', _('Servers'),
			_('olcRTC endpoints: a room on a video conferencing service and the shared encryption key of the olcrtc server in that room.'));

		let s = m.section(form.NamedSection, 'main', 'main', _('Servers in use'));
		let o = s.option(form.DynamicList, 'server', _('Failover order'),
			_('The first server is used; when it fails the next one is tried. If the list is empty the first server below is used.'));
		uci.sections('olcwrt', 'server').forEach(srv => o.value(srv['.name'], serverTitle(srv)));

		s = m.section(form.GridSection, 'server', _('Server list'));
		s.addremove = true;
		s.anonymous = true;
		s.nodescriptions = true;
		s.addbtntitle = _('Add server');
		s.modaltitle = sid => _('Server') + ' » ' + serverTitle(uci.get('olcwrt', sid) || { '.name': sid });

		s.renderRowActions = function(section_id) {
			const td = this.super('renderRowActions', [ section_id, _('Edit') ]);
			const result = E('span', { 'style': 'margin-right:.5em;font-size:90%' });
			td.lastElementChild.insertBefore(E('button', {
				'class': 'btn cbi-button cbi-button-action',
				'title': _('Start a temporary connection to this server and test it'),
				'click': ui.createHandlerFn(this, () => {
					dom.content(result, E('em', { 'class': 'spinning' }, _('Checking…')));
					return olc.runJob('check', section_id, 150).then(res => {
						dom.content(result, E('span', { 'style': res && res.ok ? 'color:#2e7d32' : 'color:#c62828' }, olc.checkSummary(res)));
					}).catch(err => dom.content(result, E('span', { 'style': 'color:#c62828' }, err.message)));
				})
			}, _('Check')), td.lastElementChild.firstChild);
			td.lastElementChild.insertBefore(result, td.lastElementChild.firstChild);
			return td;
		};

		o = s.option(form.Value, 'label', _('Name'));
		o.rmempty = true;

		o = s.option(form.ListValue, 'provider', _('Provider'));
		Object.keys(olc.providers).forEach(k => o.value(k, olc.providers[k]));
		o.default = 'jitsi';
		o.textvalue = sid => olc.providerName(uci.get('olcwrt', sid, 'provider'));

		o = s.option(form.ListValue, 'transport', _('Transport'),
			_('Jitsi works best with DataChannel; Telemost and WB Stream need VP8, SEI or Video.'));
		Object.keys(olc.transports).forEach(k => o.value(k, olc.transports[k]));
		o.default = 'datachannel';
		o.textvalue = sid => olc.transportName(uci.get('olcwrt', sid, 'transport'));

		o = s.option(form.Value, 'room', _('Room'),
			_('Jitsi: full room URL, e.g. https://meet.example.org/room. Telemost and WB Stream: the room ID created on the service website.'));
		o.rmempty = false;
		o.textvalue = function(sid) {
			const v = this.cfgvalue(sid) || '';
			return v.length > 40 ? v.substr(0, 37) + '…' : v;
		};

		o = s.option(form.Value, 'key', _('Encryption key'), _('64 hexadecimal characters, the same as on the server.'));
		o.modalonly = true;
		o.password = true;
		o.rmempty = false;
		o.validate = function(sid, v) {
			return /^[0-9a-fA-F]{64}$/.test(v || '') ? true : _('Expecting 64 hexadecimal characters');
		};

		o = s.option(form.Value, 'token', _('Provider account token'),
			_('Optional. WB Stream DataChannel needs a token with publish rights; leave empty for the guest flow.'));
		o.modalonly = true;
		o.password = true;
		o.depends('provider', 'wbstream');

		o = s.option(form.Value, 'channel', _('Channel'), _('Optional peer-routing channel ID.'));
		o.modalonly = true;

		addTransportOptions(s);

		/* cfgvalue rather than textvalue: the modal dialog renders cfgvalue. */
		o = s.option(form.DummyValue, '_subscription', _('Subscription'));
		o.cfgvalue = sid => {
			const sub = uci.get('olcwrt', sid, 'subscription');
			return sub ? (subNames[sub] || sub) : _('manual');
		};

		o = s.option(form.Value, 'comment', _('Comment'));
		o.modalonly = true;

		return m.render().then(node => E([], [
			E('div', { 'class': 'cbi-section', 'style': 'margin-bottom:0' }, [
				E('div', { 'class': 'right' }, [
					E('button', { 'class': 'btn cbi-button cbi-button-add', 'click': ui.createHandlerFn(this, 'handleImport') }, _('Import links…')), ' ',
					E('button', { 'class': 'btn cbi-button cbi-button-neutral', 'click': ui.createHandlerFn(this, 'handleExport') }, _('Export links…'))
				])
			]),
			node
		]));
	},

	handleImport() {
		const ta = E('textarea', {
			'class': 'cbi-input-textarea',
			'style': 'width:100%;font-family:monospace',
			'rows': 8,
			'placeholder': 'olcrtc://jitsi?datachannel@https://meet.example.org/room#…\nhttps://example.org/sub'
		});
		const msg = E('div');
		ui.showModal(_('Import'), [
			E('p', _('Paste olcrtc:// links (one per line), a subscription document, or subscription URLs.')),
			ta, msg,
			E('div', { 'class': 'right' }, [
				E('button', { 'class': 'btn', 'click': ui.hideModal }, _('Cancel')), ' ',
				E('button', {
					'class': 'btn cbi-button-action',
					'click': ui.createHandlerFn(this, async () => {
						dom.content(msg, E('em', { 'class': 'spinning' }, _('Importing…')));
						try {
							const res = await olc.importText(ta.value);
							const r = res.result || {};
							let text = _('Imported: %d new, %d updated').format(r.added || 0, r.updated || 0);
							let ok = true;
							if (res.job) {
								dom.content(msg, E('em', { 'class': 'spinning' }, _('Downloading the subscription…')));
								[ ok, text ] = olc.subResultsSummary(await olc.waitJob(res.job, 300));
							}
							ui.hideModal();
							ui.addNotification(null, E('p', text), ok ? 'info' : 'warning');
							window.setTimeout(() => location.reload(), 1500);
						}
						catch (err) {
							dom.content(msg, E('p', { 'style': 'color:#c62828' }, err.message));
						}
					})
				}, _('Import'))
			])
		]);
		ta.focus();
	},

	async handleExport() {
		const uris = await olc.exportURIs();
		const lines = uci.sections('olcwrt', 'server')
			.filter(s => uris[s['.name']])
			.map(s => uris[s['.name']]);
		ui.showModal(_('Export'), [
			E('p', _('Server links in the olcrtc:// format. They contain the encryption keys, share them carefully.')),
			E('textarea', { 'class': 'cbi-input-textarea', 'style': 'width:100%;font-family:monospace', 'rows': 10, 'readonly': '' }, lines.join('\n')),
			E('div', { 'class': 'right' }, E('button', { 'class': 'btn', 'click': ui.hideModal }, _('Close')))
		]);
	}
});
