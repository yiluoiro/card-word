(() => {
  "use strict";
  const dialog = document.querySelector("#project-update");
  if (!dialog) return;
  const el = name => dialog.querySelector("[data-" + name + "]");
  const openButton = document.querySelector("[data-update-open]");
  const action = el("update-action"), check = el("update-check"), bind = el("update-bind");
  const tr = (zh, en) => document.documentElement.lang.startsWith("en") ? en : zh;
  let state, checking = 0, busy = false, poll;
  function open() { if (!dialog.open) dialog.showModal(); }
  function message(text) { el("update-status").textContent = text; }
  function lock(value) {
    busy = value; check.disabled = value;
    if (action) action.disabled = value;
    if (bind) for (const e of bind.elements) e.disabled = value || !!state?.pending;
  }
  async function request(path, fields) {
    const opts = {credentials: "same-origin", cache: "no-store", headers: {Accept: "application/json"}};
    if (fields) Object.assign(opts, {method: "POST", body: new URLSearchParams(fields),
      headers: {...opts.headers, "X-CSRF-Token": dialog.dataset.csrf}});
    const res = await fetch("/admin/update" + path, opts);
    if (res.redirected) throw new Error(tr("登录已过期，请刷新页面。", "Your session expired. Reload the page."));
    const json = await res.json();
    if (!res.ok) throw new Error(json.error || ("HTTP " + res.status));
    return json;
  }
  function render(s) {
    state = s;
    const release = s.pending || (s.available && s.latest);
    el("update-target").textContent = release ? " → " + release.version : "";
    el("update-changes").hidden = !release;
    if (release) {
      el("update-notes").textContent = release.notes || tr("发布者未填写更新说明。", "No release notes were supplied.");
      el("release-link").href = release.url;
    }
    if (bind) bind.elements.repository.value = s.repository;
    if (action) {
      action.hidden = !s.pending && !(s.available && s.latest.installable);
      action.textContent = s.pending ? tr("重启并生效", "Restart to apply") : tr("安装更新", "Install update");
    }
    message(s.working ? tr("正在下载并校验程序…", "Downloading and verifying the program…") : s.pending ? tr("更新已校验。重启后启用新版本。", "Update verified. Restart to activate it.") :
      s.message || (s.available ? (!s.latest.installable ? s.latest.reason : action ?
      tr("发现新版本。安装后可选择何时重启。", "An update is available. Choose when to restart after installation.") :
      tr("发现新版本，请联系店主安装。", "An update is available. Ask the store owner to install it.")) :
      tr("当前已是最新正式版本。", "You are running the latest stable version.")));
    lock(!!s.working);
    clearTimeout(poll);
    if(s.working) poll = setTimeout(async () => { try { render(await request("")); } catch(err) { message(err.message); lock(false); } }, 1500);
  }
  async function inspect(manual) {
    if (busy) { if (manual) open(); return; }
    const seq = ++checking;
    if (manual) { open(); message(tr("正在检查更新…", "Checking for updates…")); check.disabled = true; if(action) action.hidden = true; }
    try {
      const s = await request(manual ? "?refresh=1" : "");
      if (seq !== checking) return;
      render(s); if (s.available || s.pending || s.working) open();
    } catch (err) { if (seq === checking && manual) message(err.message); }
    finally { if (seq === checking && !busy) check.disabled = false; }
  }
  openButton.addEventListener("click", () => inspect(true));
  check.addEventListener("click", () => inspect(true));
  el("update-close").addEventListener("click", () => dialog.close());
  if (bind) bind.addEventListener("submit", async event => {
    event.preventDefault();
    if (busy) return;
    ++checking;
    const repository = bind.elements.repository.value.trim();
    lock(true);
    try { await request("/repository", {repository}); state = null; lock(false); await inspect(true); }
    catch (err) { message(err.message); lock(false); }
  });
  if (action) action.addEventListener("click", async () => {
    if (!state || busy) return;
    ++checking;
    const restart = !!state.pending, release = state.pending || state.latest;
    lock(true); message(restart ? tr("正在重启…", "Restarting…") : tr("正在下载并校验程序…", "Downloading and verifying the program…"));
    try {
      await request(restart ? "/restart" : "/install", {version: release.version, repository: state.repository});
      if (!restart) { render({...state, working: true}); return; }
      for (let i = 0; i < 60; i++) {
        await new Promise(resolve => setTimeout(resolve, 1500));
        try {
          const res = await fetch("/version", {cache: "no-store", signal: AbortSignal.timeout(2500)});
          if (res.ok && (await res.json()).version === release.version) { location.reload(); return; }
        } catch (_) { /* Listener is between workers. */ }
      }
      action.hidden = true;
      message(tr("尚未确认新版本启动，请重新检查版本或查看服务日志。", "The new version has not been confirmed. Check again or inspect the service logs."));
    } catch (err) { message(err.message); }
    finally { if(!state?.working) lock(false); }
  });
  inspect(false);
})();
