// card word front-end helpers. Everything is progressive enhancement: the
// pages work without JavaScript, this file only removes friction.
(function () {
  "use strict";

  // Theme toggle: persisted per browser, defaults to light.
  // Mobile drawer: the sidebar becomes an off-canvas panel below 980px.
  (function () {
    var nav = document.getElementById("admin-nav");
    var toggle = document.querySelector("[data-nav-toggle]");
    var scrim = document.querySelector("[data-nav-scrim]");
    if (!nav || !toggle) return;

    function setOpen(open) {
      nav.classList.toggle("open", open);
      toggle.setAttribute("aria-expanded", open ? "true" : "false");
      if (scrim) {
        scrim.hidden = !open;
        scrim.classList.toggle("show", open);
      }
      document.body.style.overflow = open ? "hidden" : "";
    }

    toggle.addEventListener("click", function () {
      setOpen(!nav.classList.contains("open"));
    });
    if (scrim) scrim.addEventListener("click", function () { setOpen(false); });
    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape") setOpen(false);
    });
    nav.addEventListener("click", function (event) {
      if (event.target.closest("a")) setOpen(false);
    });
    window.addEventListener("resize", function () {
      if (window.innerWidth > 980) setOpen(false);
    });
  })();

  var root = document.documentElement;
  var savedTheme = null;
  try { savedTheme = window.localStorage.getItem("cw-theme"); } catch (e) { /* private mode */ }
  if (savedTheme === "dark" || savedTheme === "light") {
    root.setAttribute("data-theme", savedTheme);
  }
  document.querySelectorAll("[data-theme-toggle]").forEach(function (button) {
    button.addEventListener("click", function () {
      var next = root.getAttribute("data-theme") === "dark" ? "light" : "dark";
      root.setAttribute("data-theme", next);
      try { window.localStorage.setItem("cw-theme", next); } catch (e) { /* ignore */ }
    });
  });

  // Auto-submit payment forms (provider redirect handoff).
  // Turn flash messages into dismissible toasts so they do not shift layout.
  document.querySelectorAll("[data-toast]").forEach(function (el) {
    var kind = el.getAttribute("data-toast") === "err" ? "err" : "ok";
    var toast = document.createElement("div");
    toast.className = "toast " + kind;
    toast.innerHTML = el.innerHTML;
    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "关闭");
    close.textContent = "\u00d7";
    close.addEventListener("click", function () { toast.remove(); });
    toast.appendChild(close);
    document.body.appendChild(toast);
    el.style.display = "none";
    window.setTimeout(function () { toast.remove(); }, 6000);
  });

  document.querySelectorAll("form[data-autosubmit]").forEach(function (form) {
    form.submit();
  });

  // Auto-follow redirect actions.
  document.querySelectorAll("[data-redirect]").forEach(function (el) {
    var url = el.getAttribute("data-redirect");
    if (url) {
      window.setTimeout(function () {
        window.location.href = url;
      }, 400);
    }
  });

  // Destructive action confirmation.
  document.querySelectorAll("[data-check-all]").forEach(function (box) {
    box.addEventListener("change", function () {
      var target = box.getAttribute("data-check-all");
      document.querySelectorAll('input[form="' + target + '"]').forEach(function (cb) {
        cb.checked = box.checked;
      });
    });
  });

  document.querySelectorAll("form[data-confirm]").forEach(function (form) {
    form.addEventListener("submit", function (event) {
      if (!window.confirm(form.getAttribute("data-confirm"))) {
        event.preventDefault();
      }
    });
  });

  // Copy helpers.
  function copyText(text, button) {
    var done = function () {
      if (!button) return;
      var original = button.textContent;
      button.textContent = "已复制";
      button.disabled = true;
      window.setTimeout(function () {
        button.textContent = original;
        button.disabled = false;
      }, 1200);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, function () {});
      return;
    }
    var area = document.createElement("textarea");
    area.value = text;
    document.body.appendChild(area);
    area.select();
    try { document.execCommand("copy"); done(); } catch (e) { /* ignore */ }
    document.body.removeChild(area);
  }

  document.querySelectorAll("[data-copy-all]").forEach(function (button) {
    button.addEventListener("click", function () {
      var root = document.querySelector(button.getAttribute("data-copy-all"));
      if (!root) return;
      var lines = Array.prototype.map.call(root.querySelectorAll("code"), function (node) {
        return node.textContent.trim();
      });
      copyText(lines.join("\n"), button);
    });
  });

  // Reveal an encrypted card key for the operator.
  document.querySelectorAll("[data-reveal]").forEach(function (button) {
    button.addEventListener("click", function () {
      var id = button.getAttribute("data-reveal");
      button.disabled = true;
      fetch("/admin/cards/" + encodeURIComponent(id) + "/reveal", {
        method: "POST",
        headers: {
          "X-CSRF-Token": document.querySelector("input[name=csrf]") ? document.querySelector("input[name=csrf]").value : "",
          "Accept": "application/json"
        }
      }).then(function (resp) {
        button.disabled = false;
        if (!resp.ok) throw new Error("failed");
        return resp.json();
      }).then(function (data) {
        window.alert("卡密：" + data.secret);
      }).catch(function () {
        button.disabled = false;
        window.alert("无法解密该卡密。");
      });
    });
  });

  // Poll the order status while a payment is pending.
  var poller = document.querySelector("[data-poll]");
  if (poller) {
    var orderNo = poller.getAttribute("data-poll");
    var token = poller.getAttribute("data-token") || "";
    var tries = 0;
    var timer = window.setInterval(function () {
      tries += 1;
      if (tries > 100) {
        window.clearInterval(timer);
        return;
      }
      fetch("/order/" + encodeURIComponent(orderNo) + "/status?token=" + encodeURIComponent(token), {
        headers: { "Accept": "application/json" }
      }).then(function (resp) {
        if (!resp.ok) return null;
        return resp.json();
      }).then(function (data) {
        if (!data) return;
        if (data.status === "delivered" || data.status === "paid") {
          window.clearInterval(timer);
          window.location.reload();
        }
        if (data.status === "closed") {
          window.clearInterval(timer);
          poller.textContent = "订单已关闭，请重新下单。";
        }
      }).catch(function () { /* keep polling */ });
    }, 5000);
  }
})();
