(function () {
  "use strict";
  if (!document.body.classList.contains("app")) return;
  ["title", "description"].forEach(function (field) {
    var input = document.querySelector("[data-seo-" + field + "]");
    var preview = document.querySelector("[data-seo-preview-" + field + "]");
    if (input && preview) input.addEventListener("input", function () {
      preview.textContent = input.value.trim() || input.placeholder;
    });
  });
  var topbar = document.querySelector(".topbar");
  function markScroll() { if (topbar) topbar.classList.toggle("is-scrolled", window.scrollY > 12); }
  window.addEventListener("scroll", markScroll, {passive:true}); markScroll();
  document.querySelectorAll("[data-admin-refresh]").forEach(function (button) {
    button.addEventListener("click", function () { window.location.reload(); });
  });
  var form = document.querySelector(".period-form");
  if (form) {
    var period = form.querySelector("[data-period-select]");
    var custom = form.querySelector(".period-custom");
    var from = form.querySelector("[name=from]");
    var to = form.querySelector("[name=to]");
    function syncDates() {
      var useCustom = period.value === "custom";
      from.disabled = to.disabled = !useCustom;
      from.required = to.required = useCustom;
      to.min = useCustom ? from.value : "";
    }
    function submitPeriod() { syncDates(); if (form.reportValidity()) form.requestSubmit(); }
    period.addEventListener("change", function () {
      syncDates();
      if (period.value === "custom") { custom.open = true; from.focus(); }
      else { custom.open = false; submitPeriod(); }
    });
    custom.addEventListener("toggle", function () {
      if (custom.open) { period.value = "custom"; syncDates(); }
    });
    from.addEventListener("change", syncDates);
    form.querySelector("[name=currency]").addEventListener("change", submitPeriod);
    form.querySelector("[data-custom-period]").addEventListener("click", function () { period.value = "custom"; syncDates(); });
    syncDates();
  }
  var help = document.querySelector(".admin-help-dialog");
  if (help && typeof help.showModal === "function") {
    document.querySelectorAll("[data-help-open]").forEach(function (button) {
      button.addEventListener("click", function () {
        var close = document.querySelector("#admin-nav.open [data-nav-close]");
        if (close) close.click();
        help.showModal();
      });
    });
    help.querySelector("[data-help-close]").addEventListener("click", function () { help.close(); });
    document.addEventListener("keydown", function (e) {
      if (help.open && (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") help.close();
    });
  }
  document.querySelectorAll("[data-line-chart]").forEach(function (chart) {
    var tip = chart.querySelector(".chart-tooltip");
    chart.querySelectorAll("[data-chart-tip]").forEach(function (point) {
      function show() { tip.textContent = window.cardwordAdminT ? window.cardwordAdminT(point.getAttribute("data-chart-tip")) : point.getAttribute("data-chart-tip"); tip.hidden = false; }
      function hide() { tip.hidden = true; }
      point.addEventListener("pointerenter", show);
      point.addEventListener("pointerleave", function () { if (document.activeElement !== point) hide(); });
      point.addEventListener("focus", show);
      point.addEventListener("blur", hide);
    });
  });
})();

document.querySelectorAll('.site-icon-panel input[type=file]').forEach(function (input) {
  var preview = input.closest('.site-icon-panel').querySelector('[data-site-icon-preview]');
  var current = preview.src;
  input.addEventListener('change', function () {
    var file = input.files[0];
    preview.src = current;
    if (!file || !['image/png','image/jpeg'].includes(file.type) || file.size > 2 * 1024 * 1024) return;
    var reader = new FileReader();
    reader.onload = function () { if (input.files[0] === file) preview.src = reader.result; };
    reader.readAsDataURL(file);
  });
});
