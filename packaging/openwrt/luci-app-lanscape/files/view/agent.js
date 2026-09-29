'use strict';
'require view';
'require form';
'require rpc';
'require ui';
'require poll';

var callStatus = rpc.declare({ object: 'lanscape', method: 'status' });
var callLast = rpc.declare({ object: 'lanscape', method: 'last' });
var callCheck = rpc.declare({ object: 'lanscape', method: 'check', params: [ 'kind' ] });

var verdictColor = { green: '#1f9d55', yellow: '#c99700', red: '#d64545', purple: '#8b5cf6', none: '#9aa4b2' };

function statusBox(st) {
	if (!st || st.error)
		return E('p', { 'class': 'alert-message warning' }, _('The agent is not running: ') + ((st && st.error) || ''));
	return E('p', {}, [
		E('strong', {}, st.connected ? _('Connected') : _('Not connected')),
		' · ', st.server || '—',
		st.agent_id ? ' · ' + st.agent_id : '',
		(!st.connected && st.last_error) ? E('div', { 'class': 'cbi-value-description' }, st.last_error) : ''
	]);
}

function resultsTable(last) {
	if (!last || last.error || !last.paths || !last.paths.length)
		return E('p', { 'class': 'cbi-value-description' }, (last && last.error) || _('No results yet.'));
	var rows = last.paths.map(function(p) {
		var cpu = p.verdict == 'purple' ? ' (' + _('limited by the router CPU') + ')' : '';
		return E('tr', { 'class': 'tr' }, [
			E('td', { 'class': 'td' }, E('span', { 'style': 'color:' + (verdictColor[p.verdict] || '#9aa4b2') }, '●')),
			E('td', { 'class': 'td' }, p.peer),
			E('td', { 'class': 'td' }, p.segment),
			E('td', { 'class': 'td' }, p.mbps ? p.mbps.toFixed(1) + ' Mbit/s' + cpu : '—'),
			E('td', { 'class': 'td' }, p.rtt_ms ? p.rtt_ms.toFixed(2) + ' ms' : '—')
		]);
	});
	return E('div', {}, [
		E('p', { 'class': 'cbi-value-description' }, _('Run') + ' #' + last.run_id + ' · ' + new Date(last.finished).toLocaleString()),
		E('table', { 'class': 'table' }, [
			E('tr', { 'class': 'tr table-titles' }, [ E('th', { 'class': 'th' }, ''), E('th', { 'class': 'th' }, _('Peer')),
				E('th', { 'class': 'th' }, _('Segment')), E('th', { 'class': 'th' }, _('Speed')), E('th', { 'class': 'th' }, _('RTT')) ])
		].concat(rows)),
		(last.problems || []).length ? E('ul', {}, last.problems.map(function(p) { return E('li', {}, p); })) : ''
	]);
}

return view.extend({
	load: function() {
		return Promise.all([ callStatus().catch(function(e) { return { error: String(e) }; }),
			callLast().catch(function(e) { return { error: String(e) }; }) ]);
	},

	render: function(data) {
		var statusNode = E('div', {}, statusBox(data[0]));
		var resultsNode = E('div', {}, resultsTable(data[1]));

		var m = new form.Map('lanscape', _('Lanscape agent'),
			_('Connects this router to a Lanscape server: network tests, DHCP leases and Wi-Fi clients as discovery sources, availability checks.'));
		var s = m.section(form.NamedSection, 'agent', 'agent', _('Settings'));
		s.addremove = false;
		var o = s.option(form.Flag, 'enabled', _('Enabled'));
		o.rmempty = false;
		o = s.option(form.Value, 'server', _('Server'), _('Gateway address of the Lanscape server, host:8443'));
		o.placeholder = 'lanscape.lan:8443';
		o = s.option(form.Value, 'token', _('Registration token'), _('From Settings → Agents → Add node; used on the first start only'));
		o.password = true;
		s.option(form.Value, 'ca_fingerprint', _('CA fingerprint'));
		s.option(form.Value, 'name', _('Name'), _('Default: the host name'));
		o = s.option(form.Value, 'exclude', _('Excluded interfaces'), _('Never used for tests; wan is excluded by default'));
		o.placeholder = 'wan*,pppoe-*';
		o = s.option(form.Value, 'max_duration', _('Test duration limit, s'));
		o.datatype = 'range(1,300)';
		o = s.option(form.Value, 'max_streams', _('Parallel streams limit'));
		o.datatype = 'range(1,64)';
		o = s.option(form.Value, 'max_udp_mbps', _('UDP rate limit, Mbit/s'));
		o.datatype = 'uinteger';
		s.option(form.Flag, 'leases', _('Discover DHCP leases')).default = '1';
		s.option(form.Flag, 'wifi', _('Discover Wi-Fi clients')).default = '1';
		s.option(form.Flag, 'forwards', _('Report port forwards')).default = '1';
		s.option(form.Flag, 'mdns', _('mDNS / DNS-SD')).default = '1';
		s.option(form.Flag, 'ssdp', _('SSDP / UPnP')).default = '1';

		var checkBtn = E('button', {
			'class': 'cbi-button cbi-button-action',
			'click': ui.createHandlerFn(this, function() {
				return callCheck('reachability').then(function(r) {
					if (r && r.error)
						ui.addNotification(null, E('p', {}, r.error), 'danger');
					else
						ui.addNotification(null, E('p', {}, _('Check started: run #') + (r && r.id)), 'info');
				});
			})
		}, _('Check from this router'));

		poll.add(function() {
			return Promise.all([ callStatus(), callLast() ]).then(function(d) {
				statusNode.replaceChildren(statusBox(d[0]));
				resultsNode.replaceChildren(resultsTable(d[1]));
			}).catch(function() {});
		}, 10);

		return m.render().then(function(form) {
			return E('div', {}, [
				E('h2', {}, _('Lanscape')),
				E('div', { 'class': 'cbi-section' }, [ E('h3', {}, _('Status')), statusNode, checkBtn ]),
				E('div', { 'class': 'cbi-section' }, [ E('h3', {}, _('Last results')), resultsNode ]),
				form
			]);
		});
	}
});
