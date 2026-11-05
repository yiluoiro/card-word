// Progressive enhancement. Forms and navigation still work without JavaScript.
(function () {
  "use strict";
  function t(text) { return window.cardwordAdminT ? window.cardwordAdminT(text) : text; }
  document.querySelectorAll('[data-product-variant]').forEach(function(select){
    select.addEventListener('change',function(){
      var form=select.form, option=select.selectedOptions[0], quantity=form.elements.quantity;
      var max=Math.min(Number(select.dataset.maxQty),Number(option.dataset.stock));
      var ready=select.dataset.ready==='true' && max>=Number(quantity.min);
      quantity.max=String(max);quantity.disabled=!ready;
      if(Number(quantity.value)>max && ready)quantity.value=String(max);
      form.querySelector('.payment-methods').disabled=!ready;
      var button=form.querySelector('button[type="submit"]');button.disabled=!ready;
      button.textContent=ready?t('立即购买'):t('库存不足，暂不可购买');
      document.querySelectorAll('[data-variant-price]').forEach(function(el){el.textContent=option.dataset.priceText;});
      document.querySelectorAll('[data-variant-stock]').forEach(function(el){el.textContent=t('库存')+' '+option.dataset.stock;});
      form.querySelector('[data-purchase-total]').dataset.unitCents=option.dataset.price;
      quantity.dispatchEvent(new Event('input',{bubbles:true}));
    });
  });
  var announcements=Array.from(document.querySelectorAll('[data-announcement-mode="popup"]'));
  function showNextAnnouncement(){
    var item=announcements.shift();if(!item)return;
    var key='cw-announcement:'+location.host+':'+(document.documentElement.dataset.shopPrefix||'')+':'+item.dataset.announcement;
    try{if(sessionStorage.getItem(key)){showNextAnnouncement();return;}}catch(_){}
    var dialog=document.createElement('dialog');dialog.className='announcement-dialog';dialog.setAttribute('aria-label',item.querySelector('summary').textContent);
    var title=document.createElement('h2');title.textContent=item.querySelector('summary').textContent;
    var content=item.querySelector('.markdown-body').cloneNode(true);var close=document.createElement('button');close.type='button';close.className='btn primary';close.textContent=t('知道了');
    close.onclick=function(){dialog.close();};dialog.append(title,content,close);document.body.append(dialog);
    dialog.addEventListener('close',function(){try{sessionStorage.setItem(key,'1');}catch(_){}dialog.remove();showNextAnnouncement();});
    dialog.showModal();close.focus();
  }
  if(announcements.length)showNextAnnouncement();
  var root = document.documentElement;
  var shopPrefix = root.dataset.shopPrefix || "";
  function syncTheme() {
    document.querySelectorAll("[data-theme-toggle]").forEach(function (button) {
      var dark = root.dataset.theme === "dark";
      button.setAttribute("aria-pressed", String(dark));
      button.setAttribute("aria-label", dark ? t("切换浅色模式") : t("切换深色模式"));
      button.title = button.getAttribute("aria-label");
    });
  }
  syncTheme();
  document.addEventListener("cw-admin-languagechange", syncTheme);
  document.querySelectorAll("[data-theme-toggle]").forEach(function (button) {
    button.addEventListener("click", function () {
      root.dataset.theme = root.dataset.theme === "dark" ? "light" : "dark";
      try { localStorage.setItem("cw-theme", root.dataset.theme); } catch (_) {}
      syncTheme();
    });
  });
  document.querySelectorAll(".nav-item.active, .shop-nav a.active").forEach(function (a) { a.setAttribute("aria-current", "page"); });

  document.querySelectorAll("[data-captcha-refresh]").forEach(function(captchaRefresh) {
    var captchaForm = captchaRefresh.closest("form");
    var captchaStatus = captchaRefresh.closest(".captcha-field").querySelector("[role=status]");
    var captchaSubmit = captchaForm.querySelector('[type="submit"]');
    var captchaExpires = Date.now() + Number(captchaForm.querySelector("[data-captcha-ttl]").dataset.captchaTtl) * 1000;
    var captchaBusy = false, captchaUncertain = false, captchaExpiryTimer;
    function captchaExpired() { return !Number.isFinite(captchaExpires) || Date.now() >= captchaExpires; }
    function scheduleCaptchaExpiry() {
      window.clearTimeout(captchaExpiryTimer);
      captchaExpiryTimer = window.setTimeout(function () {
        if (!captchaBusy) captchaStatus.textContent = t("验证码已过期，请点击验证码图片。已填的表单信息会保留。");
      }, Math.max(0, captchaExpires - Date.now()));
    }
    scheduleCaptchaExpiry();
    captchaRefresh.disabled = false;
    captchaForm.addEventListener("submit", function (event) {
      if (captchaBusy || captchaUncertain || captchaExpired()) {
        event.preventDefault();
        captchaStatus.textContent = captchaBusy ? t("正在更新验证码，请稍候。") : t("请先点击验证码图片获取有效验证码，再提交。");
        if (!captchaBusy) captchaRefresh.focus();
      }
    });
    captchaRefresh.addEventListener("click", async function () {
      if (captchaBusy) return;
      var controller = new AbortController();
      var timeout = window.setTimeout(function () { controller.abort(); }, 12000);
      var submitWasDisabled = captchaSubmit.disabled;
      captchaBusy = true; captchaRefresh.disabled = true; captchaRefresh.setAttribute("aria-busy", "true"); captchaSubmit.disabled = true;
      captchaStatus.textContent = t("正在更新验证码…");
      window.clearTimeout(captchaExpiryTimer);
      try {
        var response = await fetch(shopPrefix + (captchaForm.elements.captcha_id ? "/captcha" : "/admin/captcha"), {
          method: "POST", credentials: "same-origin", cache: "no-store", signal: controller.signal,
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body: new URLSearchParams({ csrf: captchaForm.elements.csrf.value, captcha_id: captchaForm.elements.captcha_id?.value || "", scope: captchaForm.elements.scope?.value || "" })
        });
        if (!response.ok) throw new Error(response.status === 429 ? t("刷新过于频繁，请稍后再点击验证码图片。") : t("刷新失败，请重试或重新打开此页面。"));
        var result = await response.json();
        if (!result.image || !result.image.startsWith("data:image/png;base64,") || !Number.isFinite(result.expires_in) || result.expires_in <= 0) throw new Error(t("验证码加载失败，请重试。"));
        if (captchaForm.elements.captcha_id) { if (!result.id) throw new Error("验证码加载失败"); captchaForm.elements.captcha_id.value = result.id; }
        captchaForm.querySelector("[data-captcha-image]").src = result.image;
        captchaForm.elements.captcha.value = "";
        captchaExpires = Date.now() + result.expires_in * 1000; captchaUncertain = false;
        scheduleCaptchaExpiry();
        captchaStatus.textContent = t("已更新，3 分钟内有效。请输入新图片中的字符。");
        captchaForm.elements.captcha.focus();
      } catch (error) {
        captchaUncertain = true;
        captchaStatus.textContent = error.name === "AbortError" ? t("刷新超时，请再次点击验证码图片。表单信息仍保留。") : t("刷新未完成，请再次点击验证码图片。") + error.message;
      } finally {
        window.clearTimeout(timeout);
        captchaBusy = false; captchaRefresh.disabled = false; captchaRefresh.removeAttribute("aria-busy"); captchaSubmit.disabled = submitWasDisabled;
      }
    });
  });

  var purchaseTotal = document.querySelector("[data-purchase-total]");
  if (purchaseTotal && typeof BigInt === "function") {
    var quantityInput = purchaseTotal.closest("form").elements.quantity;
    function updatePurchaseTotal() {
      var quantity = quantityInput.value;
      var valid = /^\d+$/.test(quantity) && quantityInput.checkValidity() && !quantityInput.disabled;
      purchaseTotal.querySelector("output").textContent = "—";
      if (valid) {
        var cents = BigInt(purchaseTotal.dataset.unitCents) * BigInt(quantity);
        purchaseTotal.querySelector("output").textContent = purchaseTotal.dataset.currencySymbol + (cents / BigInt(100)).toString() + "." + (cents % BigInt(100)).toString().padStart(2, "0");
      }
    }
    quantityInput.addEventListener("input", updatePurchaseTotal);
    quantityInput.addEventListener("change", updatePurchaseTotal);
    purchaseTotal.hidden = false; updatePurchaseTotal();
  }

  // Keep storefront navigation available without JS; enhance it to a disclosure on phones.
  var shopMenu = document.getElementById("shop-navigation");
  var shopMenuToggle = document.querySelector("[data-shop-menu-toggle]");
  if (shopMenu && shopMenuToggle) {
    var compactHeader = window.matchMedia("(max-width: 980px)");
    function setShopMenu(open, restore) {
      shopMenuToggle.hidden = !compactHeader.matches;
      shopMenu.hidden = compactHeader.matches && !open;
      shopMenuToggle.setAttribute("aria-expanded", String(compactHeader.matches && open));
      if (restore) shopMenuToggle.focus();
    }
    shopMenuToggle.addEventListener("click", function () { setShopMenu(shopMenu.hidden, false); });
    shopMenu.addEventListener("click", function (event) {
      if (compactHeader.matches && event.target.closest("a")) setShopMenu(false, false);
    });
    shopMenu.parentElement.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && compactHeader.matches) { event.preventDefault(); setShopMenu(false, true); }
    });
    document.addEventListener("click", function (event) {
      if (!shopMenu.hidden && compactHeader.matches && !event.target.closest(".shop-header")) setShopMenu(false, false);
    });
    compactHeader.addEventListener("change", function () { setShopMenu(false, false); });
    setShopMenu(false, false);
  }

  var smsProvider = document.querySelector("[data-sms-provider]");
  if (smsProvider) smsProvider.addEventListener("change", function () {
    document.querySelectorAll("[data-sms-panel]").forEach(function (panel) {
      panel.open = panel.dataset.smsPanel === smsProvider.value;
    });
  });

  var purchaseShortcut = document.querySelector("[data-purchase-shortcut]");
  var purchaseForm = document.getElementById("purchase");
  if (purchaseShortcut && purchaseForm && "IntersectionObserver" in window) {
    new IntersectionObserver(function (entries) {
      purchaseShortcut.hidden = entries[0].isIntersecting;
    }).observe(purchaseForm);
  }

  // Mobile navigation behaves as a modal and restores focus on dismissal.
  var nav = document.getElementById("admin-nav");
  var toggle = document.querySelector("[data-nav-toggle]");
  if (nav && toggle) {
    var scrim = document.querySelector("[data-nav-scrim]");
    var main = document.querySelector(".main");
    var close = nav.querySelector("[data-nav-close]");
    var mobile = window.matchMedia("(max-width: 980px)");
    function setOpen(open, restore) {
      open = open && mobile.matches;
      nav.classList.toggle("open", open);
      toggle.setAttribute("aria-expanded", String(open));
      nav.inert = mobile.matches && !open;
      if (main) main.inert = open;
      if (scrim) scrim.hidden = !open;
      document.body.style.overflow = open ? "hidden" : "";
      if (open) {
        nav.setAttribute("role", "dialog"); nav.setAttribute("aria-modal", "true");
        window.requestAnimationFrame(function () {
          window.requestAnimationFrame(function () {
            if (close && nav.classList.contains("open")) close.focus();
          });
        });
      } else {
        nav.removeAttribute("role"); nav.removeAttribute("aria-modal");
        if (restore) toggle.focus();
      }
    }
    toggle.addEventListener("click", function () { setOpen(!nav.classList.contains("open"), true); });
    if (close) close.addEventListener("click", function () { setOpen(false, true); });
    if (scrim) scrim.addEventListener("click", function () { setOpen(false, true); });
    document.addEventListener("keydown", function (event) {
      if (!nav.classList.contains("open")) return;
      if (event.key === "Escape") { event.preventDefault(); setOpen(false, true); }
      if (event.key === "Tab") {
        var nodes = Array.from(nav.querySelectorAll("a[href],button:not([disabled]),input:not([type=hidden])")).filter(function (el) { return el.getClientRects().length; });
        var first = nodes[0], last = nodes[nodes.length - 1];
        if (!nav.contains(document.activeElement)) { event.preventDefault(); first.focus(); }
        else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    });
    mobile.addEventListener("change", function () { setOpen(false, false); });
    setOpen(false, false);
  }

  var sidebarScroll = document.querySelector(".sidebar-scroll");
  if (sidebarScroll) {
    var currentLink = sidebarScroll.querySelector(".nav-item.active");
    if (currentLink) {
      var scrollBounds = sidebarScroll.getBoundingClientRect();
      var linkBounds = currentLink.getBoundingClientRect();
      if (linkBounds.bottom > scrollBounds.bottom || linkBounds.top < scrollBounds.top) sidebarScroll.scrollTop += linkBounds.top - scrollBounds.top - 20;
    }
  }

  document.querySelectorAll("[data-density-toggle]").forEach(function (button) {
    function syncDensity() {
      var compact = root.dataset.adminDensity === "compact";
      button.setAttribute("aria-pressed", String(compact));
      button.querySelector(".density-state").textContent = compact ? t("开启") : t("关闭");
    }
    syncDensity();
    button.addEventListener("click", function () {
      var compact = root.dataset.adminDensity !== "compact";
      root.dataset.adminDensity = compact ? "compact" : "comfortable";
      try { localStorage.setItem("cw-admin-density", root.dataset.adminDensity); } catch (_) {}
      syncDensity();
    });
  });
  // Add a keyboard stop only when a table actually needs horizontal scrolling.
  document.querySelectorAll(".app .table-wrap").forEach(function (wrap) {
    var panel = wrap.closest(".panel"), title = panel && panel.querySelector("h2,h3");
    function syncScrollRegion() {
      if (wrap.scrollWidth > wrap.clientWidth + 1) {
        wrap.tabIndex = 0; wrap.setAttribute("role", "region");
        wrap.setAttribute("aria-label", (title ? title.textContent : "数据列表") + "，可横向滚动");
      } else {
        wrap.removeAttribute("tabindex"); wrap.removeAttribute("role"); wrap.removeAttribute("aria-label");
      }
    }
    syncScrollRegion();
    if ("ResizeObserver" in window) new ResizeObserver(syncScrollRegion).observe(wrap);
  });

  var region = document.createElement("div");
  region.className = "toast-region";
  document.body.appendChild(region);
  function notify(text, kind) {
    var toast = document.createElement("div");
    toast.className = "toast " + (kind || "ok");
    toast.setAttribute("role", kind === "err" ? "alert" : "status");
    var content = document.createElement("div"); content.textContent = text;
    var close = document.createElement("button");
    close.type = "button"; close.className = "toast-close"; close.textContent = "×";
    close.setAttribute("aria-label", "关闭提示");
    close.addEventListener("click", function () { toast.remove(); });
    toast.append(content, close); region.appendChild(toast);
    if (kind !== "err") window.setTimeout(function () { if (!toast.contains(document.activeElement)) toast.remove(); }, 6000);
  }
  // Errors stay inline until the next navigation, preserving context.
  document.querySelectorAll('[data-toast="ok"]').forEach(function (el) { notify(el.textContent.trim(), "ok"); el.hidden = true; });

  document.querySelectorAll("form[data-autosubmit]").forEach(function (form) { HTMLFormElement.prototype.submit.call(form); });
  document.querySelectorAll("[data-redirect]").forEach(function (el) {
    try {
      var url = new URL(el.dataset.redirect, location.href);
      if (url.protocol === "https:" || url.protocol === "http:") window.setTimeout(function () { location.assign(url.href); }, 400);
    } catch (_) { notify("支付地址不可用，请返回订单重试。", "err"); }
  });
  document.addEventListener("submit", function (event) {
    var form = event.target;
    if (form.id && document.querySelector('[data-check-all="' + form.id + '"]')) {
      if (!Array.from(form.elements).some(function (el) { return el.name === "ids" && el.checked; })) {
        event.preventDefault(); notify("请先选择要操作的项目。", "err");
        return;
      }
    }
    var message = (event.submitter && event.submitter.dataset.confirm) || form.dataset.confirm;
    if (message && !window.confirm(message)) event.preventDefault();
  });
  document.querySelectorAll("[data-check-all]").forEach(function (all) {
    var form = document.getElementById(all.dataset.checkAll);
    if (!form) return;
    var boxes = Array.from(form.elements).filter(function (el) { return el.type === "checkbox" && el.name === "ids"; });
    var summary = document.createElement("span");
    summary.className = "bulk-selection-count";
    summary.setAttribute("role", "status");
    form.appendChild(summary);
    var clear = document.createElement("button");
    clear.type = "button"; clear.className = "btn ghost sm"; clear.textContent = t("取消选择"); clear.setAttribute("data-admin-i18n", "");
    form.appendChild(clear);
    clear.addEventListener("click", function () { boxes.forEach(function (box) { box.checked = false; }); sync(); });
    function sync() {
      var checked = boxes.filter(function (box) { return box.checked; }).length;
      all.checked = boxes.length > 0 && checked === boxes.length;
      all.indeterminate = checked > 0 && checked < boxes.length;
      all.disabled = boxes.length === 0;
      summary.textContent = t("本页已选 " + checked + " / " + boxes.length + " 项");
      summary.setAttribute("data-admin-i18n", "");
      clear.hidden = checked === 0;
      form.querySelectorAll('[type="submit"]').forEach(function (button) { button.disabled = checked === 0; });
      boxes.forEach(function (box) { var row = box.closest("tr"); if (row) row.classList.toggle("row-selected", box.checked); });
    }
    all.addEventListener("change", function () { boxes.forEach(function (box) { box.checked = all.checked; }); sync(); });
    boxes.forEach(function (box) { box.addEventListener("change", sync); });
    sync();
  });

  var command = document.querySelector(".workspace-command");
  if (command && typeof command.showModal === "function") {
    var commandInput = command.querySelector("[data-command-query]");
    var commandResults = command.querySelector("[data-command-results]");
    var commandEmpty = command.querySelector("[data-command-empty]");
    var commandItems = [], seenCommands = new Set();
    document.querySelectorAll("#admin-nav a.nav-item").forEach(function (link) {
      var url = new URL(link.href, location.href);
      if (url.origin !== location.origin || !url.pathname.startsWith(shopPrefix + "/admin") && !url.pathname.startsWith("/platform")) return;
      if (seenCommands.has(url.href)) return;
      seenCommands.add(url.href);
      commandItems.push({source: link, href: url.href});
    });
    function renderCommands() {
      var query = commandInput.value.trim().toLocaleLowerCase();
      commandResults.replaceChildren();
      commandItems.filter(function (item) { return item.source.textContent.trim().toLocaleLowerCase().includes(query); }).forEach(function (item) {
        var link = document.createElement("a"); link.href = item.href; link.textContent = item.source.textContent.trim();
        commandResults.appendChild(link);
      });
      commandEmpty.hidden = commandResults.childElementCount > 0;
    }
    function openCommands() {
      if (command.open) return;
      // Close the mobile drawer before opening another modal, releasing its focus trap.
      if (nav && nav.classList.contains("open") && close) close.click();
      commandInput.value = ""; renderCommands(); command.showModal(); commandInput.focus();
    }
    document.querySelectorAll("[data-command-open]").forEach(function (button) { button.addEventListener("click", openCommands); });
    command.querySelector("[data-command-close]").addEventListener("click", function () { command.close(); });
    commandInput.addEventListener("input", renderCommands);
    document.addEventListener("cw-admin-languagechange", function () { if (command.open) renderCommands(); });
    command.addEventListener("keydown", function (event) {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); command.close(); return; }
      var links = Array.from(commandResults.querySelectorAll("a"));
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        if (!links.length) return;
        event.preventDefault();
        var index = links.indexOf(document.activeElement);
        index = event.key === "ArrowDown" ? (index + 1) % links.length : (index <= 0 ? links.length - 1 : index - 1);
        links[index].focus();
      }
      if (event.key === "Enter" && event.target === commandInput && links.length) { event.preventDefault(); links[0].click(); }
    });
    document.addEventListener("keydown", function (event) {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") { event.preventDefault(); if (command.open) command.close(); else openCommands(); }
    });
  }

  function fallbackCopy(text) {
    var active = document.activeElement;
    var area = document.createElement("textarea");
    area.value = text; area.style.position = "fixed"; area.style.opacity = "0";
    document.body.appendChild(area); area.select();
    var copied = false;
    try { copied = document.execCommand("copy"); } finally { area.remove(); if (active) active.focus(); }
    if (!copied) throw new Error("copy failed");
  }
  document.querySelectorAll("[data-copy-all]").forEach(function (button) {
    button.addEventListener("click", async function () {
      var target = document.querySelector(button.dataset.copyAll);
      if (!target) return;
      var text = Array.from(target.querySelectorAll("code")).map(function (node) { return node.textContent.trim(); }).join("\n");
      try {
        try { if (!navigator.clipboard) throw new Error("unavailable"); await navigator.clipboard.writeText(text); }
        catch (_) { fallbackCopy(text); }
        notify("已复制，请妥善保存。", "ok");
      } catch (_) { notify("复制失败，请选中内容后手动复制。", "err"); }
    });
  });
  document.querySelectorAll("[data-reveal]").forEach(function (button) {
    button.addEventListener("click", async function () {
      button.disabled = true;
      try {
        var csrf = document.querySelector("input[name=csrf]");
        var response = await fetch(shopPrefix + "/admin/cards/" + encodeURIComponent(button.dataset.reveal) + "/reveal", {method:"POST",headers:{"X-CSRF-Token":csrf ? csrf.value : "","Accept":"application/json"}});
        if (!response.ok) throw new Error("failed");
        var data = await response.json(); window.alert("卡密：" + data.secret);
      } catch (_) { notify("无法查看卡密，请检查权限或重新登录。", "err"); }
      finally { button.disabled = false; }
    });
  });
  document.querySelectorAll("form[data-unsaved-warning]").forEach(function (form) {
    function snapshot() { return new URLSearchParams(new FormData(form)).toString(); }
    var initial = snapshot();
    var savedStatus = form.querySelector("[data-settings-status]");
    if (savedStatus) {
      function updateSavedStatus() {
        var dirty = snapshot() !== initial;
        savedStatus.textContent = dirty ? t("有未保存的修改") : t("保存后立即生效");
        savedStatus.closest(".settings-save").classList.toggle("is-dirty", dirty);
      }
      form.addEventListener("input", updateSavedStatus); form.addEventListener("change", updateSavedStatus);
    }
    var changeStatus;
    if (form.classList.contains("appearance-form")) {
      changeStatus = document.createElement("p"); changeStatus.className = "form-change-status";
      changeStatus.setAttribute("role", "status"); form.prepend(changeStatus);
      function updateChangeStatus() {
        var dirty = form.dataset.unsavedWarning === "true" || snapshot() !== initial;
        changeStatus.textContent = dirty ? t("有未发布的修改 · 预览不会保存，确认后请点击「保存并发布」。") : t("当前为已发布设置，修改后可先预览效果。");
        changeStatus.classList.toggle("is-dirty", dirty);
      }
      form.addEventListener("input", updateChangeStatus);
      form.addEventListener("change", updateChangeStatus);
      updateChangeStatus();
    }
  });

  // Sequential polling avoids overlapping requests and continues through stock replenishment.
  var poller = document.querySelector("[data-poll]");
  if (poller) {
    var attempts = 0;
    async function poll() {
      if (document.hidden) { window.setTimeout(poll, 5000); return; }
      attempts += 1;
      try {
        var response = await fetch(shopPrefix + "/order/" + encodeURIComponent(poller.dataset.poll) + "/status", {headers:{Accept:"application/json"}});
        if (response.status === 403 || response.status === 401) { poller.textContent = "查询授权已过期，请使用订单查询凭证重新访问。"; return; }
        if (response.ok) {
          var data = await response.json();
          if (data.status !== (poller.dataset.status || "pending")) {
            if (poller.hasAttribute("data-pay-page")) location.assign(shopPrefix + "/order/" + encodeURIComponent(poller.dataset.poll));
            else location.reload();
            return;
          }
        }
      } catch (_) { /* A temporary network failure can be retried. */ }
      if (attempts < 360) window.setTimeout(poll, 5000);
      else poller.textContent = "自动查询已暂停，请刷新页面查看最新状态。";
    }
    window.setTimeout(poll, 5000);
  }
})();
