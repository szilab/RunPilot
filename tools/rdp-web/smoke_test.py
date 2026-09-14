"""Browser regression test using the real embedded IronRDP JS and WASM.

Start geckodriver --port 4445, then run:
    python3 tools/rdp-web/smoke_test.py

Only the transport ticket and final SessionBuilder.connect are stubbed. No RDP
server, credentials, Python packages, or changes to RunPilot data are needed.
"""

import argparse
import functools
import http.server
import json
from pathlib import Path
import threading
import urllib.request


SCRIPT = r"""
const done = window.rdpSmokeDone;
(async () => {
  const assert = (value, message) => { if (!value) throw new Error(message); };
  const errors = [];
  window.addEventListener('error', event => errors.push(event.message));
  window.addEventListener('unhandledrejection', event => errors.push(String(event.reason)));
  $('loginDialog').close();
  refresh = async () => {};
  probeConnection = async () => {};
  const tickets = [];
  api = async (path, options) => {
    assert(path.endsWith('/transport-ticket') && options.method === 'POST', 'Unexpected API call');
    tickets.push(path);
    return {ticket: 'smoke-test-ticket'};
  };
  const rdp = await import(new URL('rdp/iron-remote-desktop-rdp-0.7.0.js', document.baseURI));
  const originalConnect = rdp.Backend.SessionBuilder.prototype.connect;
  let connections = 0, shutdowns = 0, resizes = 0, rejectRun;
  rdp.Backend.SessionBuilder.prototype.connect = async function () {
    connections++;
    if (connections === 1) throw new Error('Test connection rejected');
    return {
      desktopSize: () => ({width: 1024, height: 768}),
      run: () => new Promise((resolve, reject) => { rejectRun = reject; }),
      shutdown: () => {
        shutdowns++;
        if (connections === 2) throw new Error('Test shutdown after transport closure');
      },
      resize: () => { resizes++; },
      releaseAllInputs: () => {},
    };
  };
  try {
    const session = {id: 'smoke-rdp', targetName: 'RDP smoke test', provider: 'rdp',
      state: 'running', type: 'desktop', rdp: {host: '10.0.0.20', port: 3389, securityMode: 'automatic'}};
    remoteSessions = [session];
    const frame = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    for (let attempt = 1; attempt <= 3; attempt++) {
      session.rdp.securityMode = attempt === 3 ? 'tls' : 'automatic';
      const credentials = {username: 'test-user', domain: '', password: 'test-only-not-sent'};
      await openRemoteSession(session.id, credentials);
      await frame();
      assert(connections === attempt, 'Did not reach SessionBuilder.connect: ' + $('toast').textContent);
      assert(credentials.password === '', 'Password retained in credential object');
      assert($('remoteRDPConnectPassword').value === '', 'Password retained in input');
      assert($('remoteSessionNotice').classList.contains('hidden'), 'Unexpected blue RDP notice');
      if (attempt === 1) {
        assert($('toast').textContent.includes('Test connection rejected'), 'Connection error not shown');
        assert($('toast').classList.contains('toast-error'), 'Connection error lacks error styling');
        assert(remoteSessionID === '', 'Failed session not closed');
      } else {
        const canvas = $('remoteRDP').shadowRoot.querySelector('canvas');
        assert(getComputedStyle(canvas).visibility === 'visible', 'Connected canvas is hidden');
        assert(!$('remoteSessionView').classList.contains('hidden'), 'Session view is hidden');
        assert($('remoteLoading').classList.contains('hidden'), 'Loading overlay remains');
        assert($('toast').textContent.includes('Connected to RDP smoke test'), 'Missing connection notification');
        if (attempt === 2) {
          closeRemoteSession();
          assert(remoteSessionID === '', 'Back did not clear the active session');
          assert($('remoteSessionView').classList.contains('hidden'), 'Back did not restore the Remote screen');
          assert(!$('remoteOverview').classList.contains('hidden'), 'Remote overview remains hidden after Back');
          assert(!document.querySelector('main').classList.contains('remote-active'), 'Remote layout remains active after Back');
        }
        if (attempt === 3) {
          let rdpFocuses = 0, frameFocuses = 0;
          $('remoteRDP').focus = () => { rdpFocuses++; };
          $('remoteFrame').focus = () => { frameFocuses++; };
          document.dispatchEvent(new Event('fullscreenchange'));
          assert(rdpFocuses === 1 && frameFocuses === 0, 'Fullscreen focuses the hidden frame instead of the RDP client');
        }
      }
    }
    rejectRun(new Error('Test transport interrupted'));
    await frame();
    assert($('toast').textContent.includes('Test transport interrupted'), 'Runtime error not notified');
    assert($('toast').classList.contains('toast-error'), 'Runtime error lacks error styling');
    assert(remoteSessionID === '', 'Interrupted session not closed');
    assert(shutdowns === 2, 'Connected sessions not shut down');
    assert(resizes === 0, 'Automatic RDP resize must remain disabled');
    assert(tickets.length === 3, 'Missing per-attempt transport ticket');
    assert(errors.length === 0, 'Browser errors: ' + errors.join('; '));
    return {connections, shutdowns, resizes, basePath: location.pathname};
  } finally {
    rdp.Backend.SessionBuilder.prototype.connect = originalConnect;
    closeRemoteSession();
  }
})().then(result => done({result})).catch(error => done({error: error.message, stack: error.stack}));
"""


class Handler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path.startswith('/pilot/'):
            self.path = self.path[len('/pilot'):]
        super().do_GET()

    def log_message(self, *_args):
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--webdriver', default='http://127.0.0.1:4445')
    args = parser.parse_args()

    def webdriver(path, payload=None, method='POST'):
        request = urllib.request.Request(
            args.webdriver + path,
            data=json.dumps(payload).encode() if payload is not None else None,
            headers={'Content-Type': 'application/json'}, method=method)
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)['value']

    static = Path(__file__).resolve().parents[2] / 'internal/web/static'
    server = http.server.ThreadingHTTPServer(
        ('127.0.0.1', 0), functools.partial(Handler, directory=str(static)))
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        for base_path in ('/', '/pilot/'):
            session = webdriver('/session', {'capabilities': {'alwaysMatch': {
                'browserName': 'firefox', 'moz:firefoxOptions': {'args': ['-headless']}}}})['sessionId']
            prefix = '/session/' + session
            try:
                webdriver(prefix + '/timeouts', {'script': 30000})
                webdriver(prefix + '/url', {'url': f'http://127.0.0.1:{server.server_port}{base_path}'})
                # Execute in the page realm to access app.js's global lexical state.
                result = webdriver(prefix + '/execute/async', {'script': '''
                    window.rdpSmokeDone = arguments[arguments.length - 1];
                    const script = document.createElement('script');
                    script.textContent = arguments[0];
                    document.body.append(script);
                ''', 'args': [SCRIPT]})
                if 'error' in result:
                    raise AssertionError(json.dumps(result, indent=2))
                print('PASS:', json.dumps(result['result']))
            finally:
                webdriver(prefix, method='DELETE')
    finally:
        server.shutdown()
        server.server_close()


if __name__ == '__main__':
    main()
