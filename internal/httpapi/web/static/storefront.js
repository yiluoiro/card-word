(function () {
  "use strict";
  if (document.body.matches(".app,.admin-auth")) return;
  function preferredLanguage() {
    try {
      var saved = localStorage.getItem("cw-language-preference");
      if (saved === "zh" || saved === "en") return saved;
      // Older versions stored English even without a choice. Only their Chinese
      // value proves an explicit selection; let old defaults follow the browser.
      if (localStorage.getItem("cw-language") === "zh") return "zh";
    } catch (_) { /* Browser preferences still work when storage is blocked. */ }
    var preferences = navigator.languages && navigator.languages.length ? navigator.languages : [navigator.language || "en"];
    for (var i = 0; i < preferences.length; i++) {
      var base = String(preferences[i]).toLowerCase().split(/[-_]/)[0];
      if (base === "zh" || base === "en") return base;
    }
    return "en";
  }
  var language = preferredLanguage();

  var header = document.querySelector(".shop-header");
  var backToTop = document.querySelector("[data-back-to-top]");
  var purchaseBar = document.querySelector(".mobile-purchase");
  if (header && backToTop) {
    function updateOffsets() {
      var menuToggle = header.querySelector("[data-shop-menu-toggle]");
      // Keep anchor offsets tied to the collapsed navigation, even while its
      // mobile menu is open. Following a link closes that menu before scrolling.
      if (!menuToggle || menuToggle.getAttribute("aria-expanded") !== "true") {
        document.documentElement.style.setProperty("--shop-header-height", header.getBoundingClientRect().height + "px");
      }
      backToTop.style.setProperty("--purchase-bar-height", (purchaseBar ? purchaseBar.getBoundingClientRect().height : 0) + "px");
    }
    function updateBackToTop() { backToTop.hidden = window.scrollY < 300; }
    window.addEventListener("scroll", updateBackToTop, { passive: true });
    window.addEventListener("pageshow", updateBackToTop);
    window.addEventListener("resize", updateOffsets);
    if ("ResizeObserver" in window) {
      var sizeObserver = new ResizeObserver(updateOffsets);
      sizeObserver.observe(header);
      if (purchaseBar) sizeObserver.observe(purchaseBar);
    }
    backToTop.addEventListener("click", function () {
      // Move keyboard focus to the visible header before this control disappears.
      var brand = header.querySelector(".shop-brand");
      if (brand) brand.focus({ preventScroll: true });
      window.scrollTo({ top: 0, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "instant" : "smooth" });
    });
    updateOffsets();
    updateBackToTop();
  }

  function applyLanguage(next) {
    language = next === "zh" ? "zh" : "en";
    document.querySelectorAll("[data-en][data-zh]").forEach(function (node) {
      node.textContent = node.getAttribute("data-" + language);
      node.lang = language === "zh" ? "zh-CN" : "en";
    });
    document.querySelectorAll("[data-en-placeholder][data-zh-placeholder]").forEach(function (node) {
      node.placeholder = node.getAttribute("data-" + language + "-placeholder");
      node.setAttribute("aria-label", node.placeholder);
    });
    document.querySelectorAll("[data-language-label]").forEach(function (node) {
      node.textContent = language === "zh" ? "中文" : "English";
    });
    document.querySelectorAll("[data-language]").forEach(function (node) {
      node.setAttribute("aria-pressed", String(node.dataset.language === language));
    });
    // Account, checkout and merchant forms retain their existing Chinese copy.
    if (document.body.classList.contains("reference-store")) {
      document.documentElement.lang = language === "zh" ? "zh-CN" : "en";
    }
  }
  applyLanguage(language);
  document.querySelectorAll("[data-language]").forEach(function (button) {
    button.addEventListener("click", function () {
      applyLanguage(button.dataset.language);
      // Only explicit choices override future browser preference changes.
      try { localStorage.setItem("cw-language-preference", language); } catch (_) {}
      var menu = button.closest("details");
      if (menu) { menu.open = false; menu.querySelector("summary").focus(); }
    });
  });
  window.addEventListener("languagechange", function () { applyLanguage(preferredLanguage()); });
  var dropdowns = document.querySelectorAll(".shop-nav .nav-dropdown");
  dropdowns.forEach(function (menu) {
    var summary = menu.querySelector("summary");
    var panel = menu.querySelector(".nav-dropdown-panel");
    var mouseInside = false;
    var closeTimer;
    function scheduleClose() {
      clearTimeout(closeTimer);
      closeTimer = setTimeout(function () {
        if (!mouseInside && !panel.contains(document.activeElement)) menu.open = false;
      }, 180);
    }
    menu.addEventListener("pointerenter", function (event) {
      if (event.pointerType !== "mouse") return;
      mouseInside = true;
      clearTimeout(closeTimer);
      dropdowns.forEach(function (other) { if (other !== menu) other.open = false; });
      menu.open = true;
    });
    menu.addEventListener("pointerleave", function (event) {
      if (event.pointerType !== "mouse") return;
      mouseInside = false;
      scheduleClose();
    });
    summary.addEventListener("click", function (event) {
      // A mouse click after hovering should not collapse the newly opened menu.
      // Native details activation remains available for touch and keyboard.
      if (mouseInside && event.detail > 0) {
        event.preventDefault();
        menu.open = true;
      }
    });
    menu.addEventListener("focusout", scheduleClose);
  });
  document.addEventListener("click", function (event) {
    document.querySelectorAll(".nav-dropdown[open]").forEach(function (menu) {
      if (!menu.contains(event.target) || event.target.closest("a")) menu.open = false;
    });
  });
  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape") return;
    document.querySelectorAll(".nav-dropdown[open]").forEach(function (menu) {
      menu.open = false;
      if (menu.contains(document.activeElement)) menu.querySelector("summary").focus();
    });
  });
})();
