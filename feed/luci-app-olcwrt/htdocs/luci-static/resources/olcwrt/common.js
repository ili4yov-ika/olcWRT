'use strict';
'require baseclass';
'require rpc';
'require ui';

const callStatus = rpc.declare({ object: 'luci.olcwrt', method: 'status' });
const callLogs = rpc.declare({ object: 'luci.olcwrt', method: 'logs', params: [ 'lines' ], expect: { lines: [] } });
const callImport = rpc.declare({ object: 'luci.olcwrt', method: 'import', params: [ 'text' ] });
const callExport = rpc.declare({ object: 'luci.olcwrt', method: 'export', expect: { uris: {} } });
const callJobStart = rpc.declare({ object: 'luci.olcwrt', method: 'job_start', params: [ 'kind', 'arg' ] });
const callJobGet = rpc.declare({ object: 'luci.olcwrt', method: 'job_get', params: [ 'id' ] });
const callReload = rpc.declare({ object: 'luci.olcwrt', method: 'reload' });
const callInitAction = rpc.declare({
	object: 'luci',
	method: 'setInitAction',
	params: [ 'name', 'action' ],
	expect: { result: false }
});

const providers = {
	jitsi: 'Jitsi',
	telemost: 'Yandex Telemost',
	wbstream: 'WB Stream'
};

const transports = {
	datachannel: 'DataChannel',
	vp8channel: 'VP8',
	seichannel: 'SEI (H.264)',
	videochannel: 'Video (QR/tile)'
};

function stateInfo(state) {
	switch (state) {
	case 'connected':    return { label: _('Connected'), color: '#2e7d32' };
	case 'connecting':   return { label: _('Connecting'), color: '#ef6c00' };
	case 'starting':     return { label: _('Starting'), color: '#ef6c00' };
	case 'waiting_peer': return { label: _('Waiting for the server in the room'), color: '#ef6c00' };
	case 'reconnecting': return { label: _('Reconnecting'), color: '#ef6c00' };
	case 'error':        return { label: _('Error'), color: '#c62828' };
	case 'idle':         return { label: _('No server selected'), color: '#757575' };
	case 'disabled':     return { label: _('Disabled'), color: '#757575' };
	default:             return { label: _('Stopped'), color: '#757575' };
	}
}

function sleep(ms) {
	return new Promise(resolve => window.setTimeout(resolve, ms));
}

function rpcError(res) {
	if (res && res.error)
		throw new Error(res.error);
	return res;
}

return baseclass.extend({
	providers: providers,
	transports: transports,

	status() {
		return L.resolveDefault(callStatus(), {});
	},

	logs(lines) {
		return L.resolveDefault(callLogs(lines || 300), []);
	},

	importText(text) {
		return callImport(text).then(rpcError);
	},

	exportURIs() {
		return L.resolveDefault(callExport(), {});
	},

	reload() {
		return L.resolveDefault(callReload(), {});
	},

	initAction(action) {
		return callInitAction('olcwrt', action);
	},

	/* Starts a background job on the router and resolves with its result. */
	async runJob(kind, arg, timeoutSec) {
		const started = rpcError(await callJobStart(kind, arg || ''));
		if (!started || !started.id)
			throw new Error(_('The job could not be started'));
		return this.waitJob(started.id, timeoutSec);
	},

	async waitJob(id, timeoutSec) {
		const deadline = Date.now() + (timeoutSec || 240) * 1000;
		while (Date.now() < deadline) {
			await sleep(1000);
			const job = await L.resolveDefault(callJobGet(id), null);
			if (job && job.state == 'done') {
				if (job.error)
					throw new Error(job.error);
				return job.result;
			}
		}
		throw new Error(_('Timed out waiting for the router'));
	},

	/* Summarises subscription refresh results; returns [ok, text]. */
	subResultsSummary(results) {
		const failed = (results || []).filter(r => !r.ok);
		if (failed.length)
			return [ false, failed.map(r => '%s: %s'.format(r.id, r.error)).join('; ') ];
		const total = (results || []).reduce((n, r) => n + (r.total || 0), 0);
		return [ true, _('Subscriptions updated: %d servers').format(total) ];
	},

	stateInfo: stateInfo,

	stateBadge(state) {
		const info = stateInfo(state);
		return E('span', {
			'style': 'display:inline-block;padding:2px 10px;border-radius:10px;color:#fff;font-weight:bold;background:%s'.format(info.color)
		}, info.label);
	},

	providerName(p) {
		return providers[p] || p || '-';
	},

	transportName(t) {
		return transports[t] || t || '-';
	},

	formatBytes(n) {
		n = +n || 0;
		const units = [ 'B', 'KiB', 'MiB', 'GiB', 'TiB' ];
		let i = 0;
		while (n >= 1024 && i < units.length - 1) {
			n /= 1024;
			i++;
		}
		return i ? '%.1f %s'.format(n, units[i]) : '%d %s'.format(n, units[i]);
	},

	formatDuration(sec) {
		sec = Math.max(0, Math.floor(+sec || 0));
		const d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600),
		      m = Math.floor(sec % 3600 / 60), s = sec % 60;
		if (d)
			return _('%dd %dh').format(d, h);
		if (h)
			return _('%dh %dm').format(h, m);
		if (m)
			return _('%dm %ds').format(m, s);
		return _('%ds').format(s);
	},

	formatTime(unix) {
		if (!unix)
			return _('never');
		const d = new Date(unix * 1000);
		return '%04d-%02d-%02d %02d:%02d'.format(d.getFullYear(), d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes());
	},

	notifyError(err) {
		ui.addNotification(null, E('p', [ _('Error'), ': ', (err && err.message) || String(err) ]), 'error');
	},

	/* Renders the result of a connectivity check. */
	checkSummary(res) {
		if (!res)
			return _('No result');
		if (!res.ok)
			return _('Failed: %s').format(res.error || ('HTTP ' + res.status));
		let text = _('OK, %d ms').format(res.latency_ms || 0);
		if (res.connect_ms)
			text += ', ' + _('session in %.1f s').format(res.connect_ms / 1000);
		if (res.ip)
			text += ', ' + _('exit IP %s').format(res.ip);
		return text;
	}
});
