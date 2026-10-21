'use strict';
'require view';
'require poll';
'require ui';
'require olcwrt.common as olc';

return view.extend({
	load() {
		return olc.logs(500);
	},

	render(lines) {
		this.follow = true;
		this.logNode = E('textarea', {
			'class': 'cbi-input-textarea',
			'style': 'width:100%;font-family:monospace;font-size:12px',
			'rows': 30,
			'readonly': '',
			'wrap': 'off'
		});
		this.show(lines);

		const followBox = E('input', { 'type': 'checkbox', 'checked': '', 'change': ev => { this.follow = ev.target.checked; } });

		poll.add(() => olc.logs(500).then(l => this.show(l)), 5);

		return E([], [
			E('h2', _('Log')),
			E('div', { 'class': 'cbi-map-descr' },
				_('Messages of olcWRT and olcrtc. The log is kept in RAM and limited in size; per-connection messages appear only with the Debug log level.')),
			E('div', { 'style': 'margin-bottom:.5em' }, [
				E('label', {}, [ followBox, ' ', _('Follow') ]), ' ',
				E('button', {
					'class': 'btn cbi-button',
					'click': ui.createHandlerFn(this, () => olc.logs(500).then(l => this.show(l)))
				}, _('Refresh'))
			]),
			this.logNode
		]);
	},

	show(lines) {
		const text = (lines || []).join('\n');
		if (this.logNode.value === text)
			return;
		this.logNode.value = text || _('The log is empty.');
		if (this.follow)
			this.logNode.scrollTop = this.logNode.scrollHeight;
	},

	handleSaveApply: null,
	handleSave: null,
	handleReset: null
});
