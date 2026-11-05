(function () {
  "use strict";
  var root = document.documentElement;
  if (!document.body.matches(".app,.admin-auth")) return;
  var messages = window.cardwordAdminMessages || {};
  var originalTitle = document.title;
  var language = "zh";
  var originals = {};
  Object.keys(messages).forEach(function (key) { originals[messages[key]] = key; });
  var textSources = new WeakMap(), attributeSources = new WeakMap();
  function preferredLanguage() {
    try {
      var saved = localStorage.getItem("cw-language-preference");
      if (saved === "zh" || saved === "en") return saved;
    } catch (_) {}
    var languages = navigator.languages || [navigator.language];
    for (var value of languages) {
      var base = String(value).toLowerCase().split(/[-_]/)[0];
      if (base === "zh" || base === "en") return base;
    }
    return "en";
  }
  function translate(text) {
    if (language !== "en") return text;
    if (Object.prototype.hasOwnProperty.call(messages, text)) return messages[text];
    var selected = /^本页已选 (\d+) \/ (\d+) 项$/.exec(text);
    if (selected) return "Selected " + selected[1] + " / " + selected[2] + " on this page";
    var chart = /^(\d{2}-\d{2}) · (\d+) 单 · (.+)$/.exec(text);
    if (chart) return chart[1] + " · " + chart[2] + " orders · " + chart[3];
    var match = /^较上一周期 ([+-]?[\d.]+%)$/.exec(text);
    if (match) return match[1] + " vs previous period";
    return text;
  }
  window.cardwordAdminT = translate;
  function sourceText(text) {
    var selected = /^Selected (\d+) \/ (\d+) on this page$/.exec(text);
    if (selected) return "本页已选 " + selected[1] + " / " + selected[2] + " 项";
    return originals[text.trim()] || text;
  }
  function translateText(element) {
    if (element.tagName === "OPTION" && !element.hasAttribute("value")) element.value = element.textContent;
    // Only direct UI text nodes: preserve icons, descendants and event listeners.
    Array.from(element.childNodes).forEach(function (node) {
      if (node.nodeType !== Node.TEXT_NODE) return;
      var state = textSources.get(node);
      if (!state || node.data !== state.output) state = {source:sourceText(node.data), output:node.data};
      var trimmed = state.source.trim();
      var result = state.source.replace(trimmed, translate(trimmed));
      state.output = result; textSources.set(node, state);
      if (node.data !== result) node.data = result;
    });
  }
  function render() {
    document.querySelectorAll("[data-admin-i18n],[data-admin-i18n-option],.density-state,[data-settings-status],#captcha-help,.bulk-selection-count").forEach(translateText);
    document.querySelectorAll("[data-admin-i18n-attrs]").forEach(function (el) {
      var states = attributeSources.get(el) || {};
      el.getAttribute("data-admin-i18n-attrs").split(/\s+/).forEach(function (attr) {
        var current = el.getAttribute(attr);
        if (current === null) return;
        var state = states[attr];
        if (!state || current !== state.output) state = {source:originals[current] || current};
        state.output = translate(state.source); states[attr] = state;
        if (current !== state.output) el.setAttribute(attr, state.output);
      });
      attributeSources.set(el, states);
    });
  }
  function apply(next) {
    language = next === "zh" ? "zh" : "en";
    var titleParts = originalTitle.split(" · ");
    titleParts[0] = translate(titleParts[0]); document.title = titleParts.join(" · ");
    root.lang = language === "zh" ? "zh-CN" : "en";
    root.dataset.adminLanguage = language;
    render();
    document.querySelectorAll("[data-admin-language-label]").forEach(function (label) { label.textContent = language === "zh" ? "简体中文" : "English"; });
    document.querySelectorAll("[data-admin-language-toggle]").forEach(function (toggle) { toggle.setAttribute("aria-label", language === "zh" ? "选择后台语言" : "Choose admin language"); });
    document.querySelectorAll("[data-admin-language]").forEach(function (button) { button.setAttribute("aria-pressed", String(button.dataset.adminLanguage === language)); });
    document.dispatchEvent(new CustomEvent("cw-admin-languagechange", {detail:language}));
  }
  var menus = document.querySelectorAll("[data-admin-language-menu]");
  menus.forEach(function (menu) {
    var summary = menu.querySelector("summary"), timer, mouseInside = false;
    menu.addEventListener("pointerenter", function (event) { if (event.pointerType === "mouse") { mouseInside = true; clearTimeout(timer); menu.open = true; } });
    menu.addEventListener("pointerleave", function () { mouseInside = false; timer = setTimeout(function () { if (!menu.contains(document.activeElement)) menu.open = false; }, 160); });
    summary.addEventListener("click", function (event) { if (mouseInside && event.detail > 0) { event.preventDefault(); menu.open = true; } });
    menu.addEventListener("keydown", function (event) {
      if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); menu.open = false; summary.focus(); }
      if (event.key === "ArrowDown" && event.target === summary) { event.preventDefault(); menu.open = true; menu.querySelector("button").focus(); }
    });
    menu.querySelectorAll("[data-admin-language]").forEach(function (button) {
      button.addEventListener("click", function () {
        apply(button.dataset.adminLanguage);
        try { localStorage.setItem("cw-language-preference", language); } catch (_) {}
        clearTimeout(timer); menu.open = false; summary.focus();
      });
    });
  });
  document.addEventListener("click", function (event) { menus.forEach(function (menu) { if (!menu.contains(event.target)) menu.open = false; }); });
  window.addEventListener("languagechange", function () { apply(preferredLanguage()); });
  window.addEventListener("storage", function (event) { if (event.key === "cw-language-preference") apply(preferredLanguage()); });
  apply(preferredLanguage());
  // Dynamic feedback is translated only inside explicitly marked UI containers.
  var queued = false;
  var observer = new MutationObserver(function () {
    if (queued) return;
    queued = true;
    queueMicrotask(function () { queued = false; render(); });
  });
  observer.observe(document.body, {childList:true, subtree:true, characterData:true, attributes:true, attributeFilter:["aria-label","title","placeholder","data-label"]});
})();
