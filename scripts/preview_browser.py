"""Check synthetic preview pages exported by TestExportSingleStorePreview.

Requires Python Playwright with Chromium. Does not submit forms or load live data.
"""
import functools
import http.server
import json
import os
from pathlib import Path
import threading
from playwright.sync_api import sync_playwright

root = Path(os.environ['CARDWORD_COMMUNITY_PREVIEW_DIR']).resolve()
output = root.parent / 'screenshots'
output.mkdir(exist_ok=True)

class Quiet(http.server.SimpleHTTPRequestHandler):
    def log_message(self, *args):
        pass

server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), functools.partial(Quiet, directory=str(root)))
threading.Thread(target=server.serve_forever, daemon=True).start()
base = f'http://127.0.0.1:{server.server_port}'
report = []
try:
    with sync_playwright() as pw:
        browser = pw.chromium.launch()
        for width in [1440, 390]:
            context = browser.new_context(viewport={'width': width, 'height': 960}, locale='zh-CN', reduced_motion='reduce')
            context.add_init_script("localStorage.setItem('cw-language-preference','zh');")
            page = context.new_page()
            errors = []
            page.on('pageerror', lambda e: errors.append(str(e)))
            page.route('**/*', lambda r: r.continue_() if r.request.url.startswith(base + '/') and r.request.method == 'GET' else r.abort())
            for file in sorted(root.rglob('index.html')):
                path = '/' + file.relative_to(root).as_posix().replace('index.html', '')
                page.goto(base + path, wait_until='networkidle')
                page.evaluate("() => Promise.all([...document.images].map(i => {i.loading='eager';return i.decode().catch(()=>{});} ))")
                result = page.evaluate('''() => ({
                    overflow: document.documentElement.scrollWidth > innerWidth + 2,
                    brokenImages: [...document.images].filter(i => i.getClientRects().length && !i.naturalWidth).map(i => i.src),
                    removedLinks: [...document.querySelectorAll('a[href]')].map(a => a.getAttribute('href')).filter(h => /^\\/(platform|merchant|stores|reports|s\\/)|^\\/admin\\/(wallet|complaints|market\\/releases)/.test(h))
                })''')
                name = path.strip('/').replace('/', '-') or 'storefront'
                page.screenshot(path=str(output / f'{name}-{width}.png'), full_page=True)
                report.append({'path': path, 'width': width, **result})
            if errors:
                raise AssertionError(errors)
            context.close()
        browser.close()
finally:
    server.shutdown()
(output / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding='utf-8')
failures = [row for row in report if row['overflow'] or row['brokenImages'] or row['removedLinks']]
if failures:
    raise SystemExit(json.dumps(failures, ensure_ascii=False, indent=2))
print(f'{len(report)} page/viewport checks passed; screenshots: {output}')
