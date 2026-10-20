// Functional DOM test against an actual, isolated Go daemon. No hosted calls.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { mkdtemp, writeFile, mkdir, rm } = require('node:fs/promises');
const { tmpdir } = require('node:os');
const { join, resolve } = require('node:path');
const { spawn, execFileSync } = require('node:child_process');
const net = require('node:net');
const { JSDOM, VirtualConsole } = require('jsdom');

async function waitFor(predicate, timeout = 10000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    if (await predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  throw new Error('Timed out waiting for dashboard state');
}

test('dashboard renders real API state and supports search and project form', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'stalemate-ui-'));
  const socket = net.createServer();
  await new Promise(resolve => socket.listen(0, '127.0.0.1', resolve));
  const port = socket.address().port;
  await new Promise(resolve => socket.close(resolve));
  const cfg = join(dir, 'config.json');
  const env = join(dir, 'env');
  await writeFile(env, '');
  await writeFile(cfg, JSON.stringify({
    data_dir: join(dir, 'data'),
    http: { listen: `127.0.0.1:${port}`, token_env: 'STALEMATE_UI_TEST_TOKEN' },
    ai: { provider: 'disabled' },
    capture: { codex_home: join(dir, 'empty-codex-home'), poll_seconds: 1 },
  }));
  const childEnv = { ...process.env, STALEMATE_UI_TEST_TOKEN: '' };
  delete childEnv.STALEMATE_DATA_DIR;
  delete childEnv.STALEMATE_DATABASE_URL;
  const child = spawn(resolve('bin/stalemate'), ['--config', cfg, '--env-file', env, 'serve'], {
    env: childEnv, stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderr = '';
  child.stderr.on('data', b => { stderr += b; });
  let dom;
  let pendingAction;
  try {
    const base = `http://127.0.0.1:${port}`;
    await waitFor(async () => {
      if (child.exitCode !== null) throw Error(stderr);
      try { return (await fetch(base + '/healthz')).ok; } catch { return false; }
    });
    const project = join(dir, 'demo');
    await mkdir(project);
    execFileSync('git', ['init', '-b', 'master', project], { stdio: 'ignore' });
    await writeFile(join(project, 'go.mod'), 'module example.com/dashboard-demo\n\ngo 1.25.3\n');
    await writeFile(join(project, 'demo.go'), 'package demo\nfunc Ready() {}\n');
    const registered = await fetch(base + '/api/projects', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ path: project, name: 'Dashboard demo', cloud_allowed: false }),
    });
    assert.equal(registered.status, 201);
    const html = await (await fetch(base)).text();
    const errors = [];
    const console = new VirtualConsole();
    console.on('jsdomError', error => errors.push(error));
    dom = new JSDOM(html, {
      url: base, runScripts: 'dangerously', pretendToBeVisual: true, virtualConsole: console,
      beforeParse(window) {
        window.fetch = (path, options) => fetch(new URL(path, base), options);
        // Explicit actions exercise refresh; background polling is unnecessary
        // here and would race teardown of the simulated browser.
        window.setInterval = () => 0;
      },
    });
    const action = dom.window.action;
    dom.window.action = fn => (pendingAction = action(fn));
    const doc = dom.window.document;
    await waitFor(() => doc.getElementById('live').textContent === 'Connected');
    assert.equal(doc.getElementById('project-count').textContent, '1');
    assert.equal(doc.getElementById('alert-count').textContent, '0');
    assert.match(doc.getElementById('projects').textContent, /Dashboard demo/);
    assert.match(doc.getElementById('projects').textContent, /Local capture only/);
    assert.match(doc.getElementById('graph').textContent, /Dashboard demo/);
    assert.equal(doc.getElementById('error').classList.contains('hidden'), true);
    doc.getElementById('query').value = 'a missing decision';
    doc.getElementById('search-form').dispatchEvent(new dom.window.Event('submit', { cancelable: true }));
    await waitFor(() => doc.getElementById('results').textContent.includes('No matching evidence.'));
    doc.getElementById('add-toggle').click();
    assert.equal(doc.getElementById('add-form').classList.contains('hidden'), false);
    assert.equal(doc.getElementById('cloud').checked, false);
    assert.equal(doc.querySelectorAll('script[src]').length, 0);
    await pendingAction;
    assert.deepEqual(errors, []);
  } finally {
    // Search renders its results before action's subsequent refresh finishes.
    // Drain that action before destroying document and stopping the server.
    await pendingAction;
    dom?.window.close();
    if (child.exitCode === null) {
      const closed = new Promise(resolve => child.once('exit', resolve));
      child.kill('SIGTERM');
      await closed;
    }
    await rm(dir, { recursive: true, force: true });
  }
});
