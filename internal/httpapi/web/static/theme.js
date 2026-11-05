// Apply the saved theme before the first paint; compatible with the strict CSP.
(function () {
  try {
    if (localStorage.getItem("cw-admin-density") === "compact") document.documentElement.dataset.adminDensity = "compact";
    var theme = localStorage.getItem("cw-theme");
    if (theme === "dark" || theme === "light") document.documentElement.dataset.theme = theme;
  } catch (_) { /* Storage may be unavailable. */ }
})();
