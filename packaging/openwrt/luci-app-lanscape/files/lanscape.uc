'use strict';
// rpcd backend of luci-app-lanscape: talks to the running agent through its local socket.

import { popen } from 'fs';

function agent(args) {
	const p = popen('/usr/bin/lanscape-agent ' + args + ' --local-socket /var/run/lanscape-agent.sock 2>&1');
	if (!p)
		return { error: 'cannot run lanscape-agent' };
	const out = p.read('all');
	p.close();
	try {
		return json(out);
	}
	catch (e) {
		return { error: trim(out) || 'the agent is not running' };
	}
}

return {
	lanscape: {
		status: {
			call: () => agent('status')
		},
		check: {
			args: { kind: 'reachability' },
			call: (req) => agent('check --kind ' + (req.args?.kind == 'full' ? 'full' : 'reachability'))
		},
		last: {
			call: () => agent('last')
		}
	}
};
