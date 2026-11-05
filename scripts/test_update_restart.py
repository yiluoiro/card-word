"""Opt-in browser smoke test; uses a temporary shop, builds binaries, needs Playwright Chromium."""
import contextlib
import hashlib
import hmac
import json
import os
from pathlib import Path
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
import urllib.request
from playwright.sync_api import sync_playwright, expect

ROOT = Path(__file__).resolve().parents[1]
EXT = '.exe' if os.name == 'nt' else ''

def main(tmp):
    data = tmp / 'data'
    updates = data / 'updates'
    updates.mkdir(parents=True)
    def build(version):
        path = tmp / (version + EXT)
        subprocess.run(['go', 'build', '-ldflags', '-X github.com/yiluoiro/card-word/internal/version.Version=' + version,
                        '-o', str(path), './cmd/cardword'], cwd=ROOT, check=True)
        return path
    launcher, candidate = build('v1.0.0'), build('v1.0.1')
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        addr = '127.0.0.1:' + str(sock.getsockname()[1])
    base = 'http://' + addr
    key, token, csrf = secrets.token_bytes(32), secrets.token_hex(32), secrets.token_hex(32)
    env = {**os.environ, 'CARDWORD_MASTER_KEY': key.hex(), 'CARDWORD_ADMIN_PASSWORD': secrets.token_hex(20)}
    dbpath = data / 'cardword.db'
    env['CARDWORD_DB_PATH'] = str(dbpath)
    def waitver(version):
        for _ in range(100):
            try:
                with urllib.request.urlopen(base + '/version', timeout=1) as res:
                    if json.load(res)['version'] == version:
                        return
            except OSError:
                pass
            time.sleep(.1)
        raise AssertionError('version not ready: ' + version)
    def stage(version, raw, active=None):
        # Offline fixture for the process handoff. Download integrity and staging
        # are exercised separately in internal/update against a mock transport.
        digest = hashlib.sha256(raw).hexdigest()
        path = updates / ('cardword-' + digest + EXT)
        path.write_bytes(raw)
        path.chmod(0o700)
        payload = json.dumps({'Repository': 'https://github.com/yiluoiro/card-word', 'Active': active,
                              'Pending': {'Version': version, 'Digest': digest, 'Notes': 'Test release notes',
                                          'URL': 'https://github.com/yiluoiro/card-word/releases/tag/' + version}}, separators=(',', ':'))
        mac = hmac.new(key, b'cardword/update-state/v1\x00' + payload.encode(), hashlib.sha256).hexdigest()
        (updates / 'state.json').write_text('{"Payload":' + payload + ',"MAC":"' + mac + '"}', encoding='utf-8')
    def launch():
        return subprocess.Popen([str(launcher), '--addr', addr, '--data', str(data), '--base-url', base],
                                env=env, stdout=subprocess.DEVNULL)
    proc = launch()
    try:
        waitver('v1.0.0')
        now = int(time.time())
        with contextlib.closing(sqlite3.connect(dbpath)) as db:
            uid = db.execute('SELECT id FROM users LIMIT 1').fetchone()[0]
            db.execute('INSERT INTO sessions(id,user_id,token_hash,csrf_token,ip,user_agent,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)',
                       ('test', uid, hashlib.sha256(token.encode()).hexdigest(), csrf, '127.0.0.1', 'test', now, now, now + 3600))
            db.execute("INSERT INTO settings(key,value,updated_at) VALUES('test.upgrade','preserved',?)", (now,))
            db.commit()
        with sync_playwright() as pw:
            browser = pw.chromium.launch()
            page = browser.new_page(locale='zh-CN')
            page.context.add_cookies([{'url': base, 'name': 'cw_sess', 'value': token}])
            # Exercise the browser's async download states without a live release.
            release = {'version': 'v1.0.1', 'notes': 'Release notes\n' + 'long-note-' * 100,
                       'url': 'https://github.com/yiluoiro/card-word/releases/tag/v1.0.1',
                       'installable': True}
            status = {'current': 'v1.0.0', 'repository': 'https://github.com/yiluoiro/card-word',
                      'available': True, 'latest': release, 'working': False}
            def mock_update(route):
                if route.request.url.endswith('/install'):
                    status['working'] = True
                    route.fulfill(json={'ok': True})
                else:
                    route.fulfill(json=status)
            page.route('**/admin/update*', mock_update)
            page.route('**/admin/update/install', mock_update)
            page.goto(base + '/admin')
            expect(page.locator('#project-update[open]')).to_be_visible()
            expect(page.locator('[data-update-action]')).to_have_text('安装更新')
            page.locator('[data-update-action]').click()
            expect(page.locator('[data-update-action]')).to_be_disabled()
            status.update(working=False, pending=release)
            expect(page.locator('[data-update-action]')).to_have_text('重启并生效')
            expect(page.locator('[data-update-action]')).to_be_enabled()
            page.set_viewport_size({'width': 390, 'height': 844})
            bounds = page.locator('#project-update').bounding_box()
            assert bounds['x'] >= 0 and bounds['x'] + bounds['width'] <= 390
            assert page.locator('#project-update').evaluate('(el) => el.scrollWidth <= el.clientWidth')
            page.locator('[data-update-close]').click()
            bounds = page.locator('[data-update-open]').bounding_box()
            assert bounds['x'] >= 0 and bounds['x'] + bounds['width'] <= 390
            page.locator('[data-update-open]').click()
            expect(page.locator('#project-update[open]')).to_be_visible()
            page.unroute('**/admin/update*', mock_update)
            page.unroute('**/admin/update/install', mock_update)
            page.set_viewport_size({'width': 1280, 'height': 800})
            stage('v1.0.1', candidate.read_bytes())
            page.goto(base + '/admin')
            expect(page.locator('#project-update[open]')).to_be_visible()
            expect(page.locator('[data-update-notes]')).to_have_text('Test release notes')
            page.locator('[data-update-action]').click()
            waitver('v1.0.1')
            expect(page.locator('[data-update-open]')).to_have_text('v1.0.1', timeout=15000)
            assert page.locator('[data-update-bind]').count() == 1
            active = json.loads((updates / 'state.json').read_text())['Payload']['Active']
            stage('v1.0.2', b'invalid executable fixture', active)
            page.goto(base + '/admin')
            expect(page.locator('#project-update[open]')).to_be_visible()
            page.locator('[data-update-action]').click()
            for _ in range(100):
                if json.loads((updates / 'state.json').read_text())['Payload']['Pending'] is None:
                    break
                time.sleep(.1)
            else:
                raise AssertionError('rollback did not clear trial')
            waitver('v1.0.1')
            # Even a candidate exiting successfully before readiness must roll back.
            source = tmp / 'early-exit.go'
            source.write_text('package main\nfunc main() {}', encoding='utf-8')
            early = tmp / ('early-exit' + EXT)
            subprocess.run(['go', 'build', '-o', str(early), str(source)], check=True)
            stage('v1.0.2', early.read_bytes(), active)
            page.goto(base + '/admin')
            expect(page.locator('#project-update[open]')).to_be_visible()
            page.locator('[data-update-action]').click()
            for _ in range(100):
                if json.loads((updates / 'state.json').read_text())['Payload']['Pending'] is None:
                    break
                time.sleep(.1)
            else:
                raise AssertionError('early successful exit did not roll back')
            waitver('v1.0.1')
            browser.close()
        proc.terminate()
        proc.wait(timeout=15)
        time.sleep(1)
        proc = launch()
        waitver('v1.0.1')
        with contextlib.closing(sqlite3.connect(dbpath)) as db:
            assert db.execute("SELECT value FROM settings WHERE key='test.upgrade'").fetchone() == ('preserved',)
        print('Passed: mobile dialog, install/restart buttons, manual check, restart, persistent version, session, data, and failed-start fallback.')
    finally:
        if proc.poll() is None:
            proc.terminate()
        proc.wait(timeout=15)
        time.sleep(1)

if __name__ == '__main__':
    with tempfile.TemporaryDirectory(prefix='cardword-upgrade-test-') as folder:
        main(Path(folder))
