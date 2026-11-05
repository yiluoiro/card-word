(function () {
  "use strict";
  var prefix = document.documentElement.dataset.shopPrefix || "";
  function t(value) { return window.cardwordAdminT ? window.cardwordAdminT(value) : value; }
  function size(value) { return new TextEncoder().encode(value).length; }
  document.querySelectorAll("[data-markdown-editor]").forEach(function (editor) {
    var input = editor.querySelector("textarea"), preview = editor.querySelector("[data-markdown-preview]");
    var output = editor.querySelector("[data-markdown-output]"), status = editor.querySelector("[data-markdown-status]");
    if (!input || !preview || !output || !status || input.matches(":disabled")) return;
    var mode = "edit", revision = 0, controller, timer, busy = false;
    var history = [input.value], cursor = 0, replaying = false;
    var toolbar = document.createElement("div"); toolbar.className = "md-tools"; toolbar.setAttribute("role", "toolbar"); toolbar.setAttribute("aria-label", t("Markdown 格式工具"));
    var workspace = document.createElement("div"); workspace.className = "md-workspace";
    input.before(toolbar, workspace); workspace.append(input, output);
    editor.classList.add("md-enhanced"); editor.dataset.mode = mode;
    var counter = document.createElement("span"); counter.className = "md-counter"; status.before(counter);
    function button(label, text, action) {
      var node = document.createElement("button"); node.type = "button"; node.className = "btn sm md-tool";
      node.textContent = text || t(label); node.title = t(label); node.setAttribute("aria-label", t(label)); node.dataset.mdLabel = label;
      node.addEventListener("click", action); toolbar.append(node); return node;
    }
    function report(message) { status.textContent = t(message); }
    function refreshCounter() {
      var bytes = size(input.value); counter.textContent = input.value.length.toLocaleString() + " · " + (bytes / 1024).toFixed(1) + " / 100 KB";
      counter.classList.toggle("is-over-limit", bytes > 102400);
      input.setCustomValidity(bytes > 102400 ? t("每段 Markdown 内容不能超过 100 KB。") : "");
    }
    function remember() {
      if (!replaying && history[cursor] !== input.value) {
        history = history.slice(0, cursor + 1); history.push(input.value);
        if (history.length > 100) history.shift(); cursor = history.length - 1;
      }
      undo.disabled = cursor === 0; redo.disabled = cursor === history.length - 1;
    }
    function replace(text, start, end, selectStart, selectEnd) {
      start = start == null ? input.selectionStart : start; end = end == null ? input.selectionEnd : end;
      input.setRangeText(text, start, end, "end"); input.focus();
      if (selectStart != null) input.setSelectionRange(start + selectStart, start + selectEnd);
      input.dispatchEvent(new Event("input", {bubbles:true}));
    }
    function wrap(left, right, fallback) {
      var selected = input.value.slice(input.selectionStart, input.selectionEnd) || t(fallback);
      replace(left + selected + right, null, null, left.length, left.length + selected.length);
    }
    function lines(marker) {
      var start = input.value.lastIndexOf("\n", input.selectionStart - 1) + 1;
      var end = input.value.indexOf("\n", Math.max(input.selectionStart, input.selectionEnd - 1)); if (end < 0) end = input.value.length;
      var selected = input.value.slice(start, end) || t("内容");
      var formatted = selected.split("\n").map(function (line, i) { return (typeof marker === "function" ? marker(i) : marker) + line; }).join("\n");
      replace(formatted, start, end, 0, formatted.length);
    }
    button("二级标题", "H2", function () { lines("## "); });
    button("粗体 (Ctrl+B)", "B", function () { wrap("**", "**", "重点内容"); });
    button("斜体 (Ctrl+I)", "I", function () { wrap("*", "*", "补充说明"); });
    button("删除线", "S̶", function () { wrap("~~", "~~", "旧内容"); });
    button("无序列表", "• ≡", function () { lines("- "); });
    button("有序列表", "1. ≡", function () { lines(function (i) { return (i + 1) + ". "; }); });
    button("引用", "❝", function () { lines("> "); });
    button("行内代码", "</>", function () { wrap("`", "`", "代码"); });
    button("代码块", "{ }", function () { wrap("\n```\n", "\n```\n", "代码"); });
    button("插入表格", null, function () { replace("\n| " + t("标题") + " | " + t("说明") + " |\n| --- | --- |\n| " + t("内容") + " | " + t("内容") + " |\n"); });
    button("分隔线", "—", function () { replace("\n\n---\n\n"); });
    function linkDialog() {
      var start = input.selectionStart, end = input.selectionEnd;
      var dialog = document.createElement("dialog"); dialog.className = "md-link-dialog";
      var form = document.createElement("form"); form.className = "form";
      var heading = document.createElement("h3"); heading.textContent = t("插入链接");
      var label = document.createElement("label"); label.textContent = t("链接文字");
      var text = document.createElement("input"); text.value = input.value.slice(start, end); text.required = true; label.append(text);
      var urlLabel = document.createElement("label"); urlLabel.textContent = t("链接地址");
      var url = document.createElement("input"); url.placeholder = "https://"; url.required = true; urlLabel.append(url);
      var error = document.createElement("p"); error.className = "hint"; error.setAttribute("role", "alert");
      var actions = document.createElement("div"); actions.className = "actions";
      var save = document.createElement("button"); save.type = "submit"; save.className = "btn primary"; save.textContent = t("插入");
      var cancel = document.createElement("button"); cancel.type = "button"; cancel.className = "btn"; cancel.textContent = t("取消"); cancel.onclick = function () { dialog.close(); };
      actions.append(save, cancel); form.append(heading, label, urlLabel, error, actions); dialog.append(form); document.body.append(dialog);
      dialog.addEventListener("close", function () { dialog.remove(); input.focus(); });
      form.addEventListener("submit", function (event) {
        event.preventDefault(); var value = url.value.trim();
        if (!/^(https?:\/\/|mailto:|\/(?!\/)|#)/i.test(value) || /[\s<>]/.test(value)) { error.textContent = t("请输入 HTTP、HTTPS、邮箱或本站链接。"); return; }
        var escaped = text.value.replace(/[\\\[\]]/g, "\\$&");
        replace("[" + escaped + "](" + value.replace(/\(/g, "%28").replace(/\)/g, "%29") + ")", start, end); dialog.close();
      });
      dialog.showModal(); (text.value ? url : text).focus();
    }
    button("插入链接 (Ctrl+K)", null, linkDialog);
    var picker = document.createElement("input"); picker.type = "file"; picker.accept = "image/png,image/jpeg,image/webp"; picker.hidden = true; editor.append(picker);
    var upload = button("上传图片", null, function () { picker.click(); });
    async function uploadImage(file) {
      if (busy || !file) return;
      if (file.size > 2097152) { report("图片不能超过 2 MB。"); return; }
      busy = true; upload.disabled = true; report("正在上传图片…");
      var start = input.selectionStart, end = input.selectionEnd, original = input.value;
      try {
        var body = new FormData(); body.append("image", file);
        var response = await fetch(prefix + "/admin/markdown/image", {method:"POST", credentials:"same-origin", headers:{"X-CSRF-Token":input.form.querySelector('[name=csrf]').value, "Accept":"application/json"}, body:body});
        if (response.redirected) throw new Error(t("登录已过期，请先保存文案，再重新登录。"));
        var result = await response.json(); if (!response.ok) throw new Error((result.error || {}).message || t("图片上传失败。"));
        if (!/^\/media\/[a-zA-Z0-9_.-]+$/.test(result.url)) throw new Error(t("图片上传失败。"));
        if (input.value !== original) { start = input.selectionStart; end = input.selectionEnd; }
        replace("![" + t("图片说明") + "](" + result.url + ")", start, end, 2, 2 + t("图片说明").length); report("图片已插入，请补充图片说明并保存。");
      } catch (error) { report(error.message || "图片上传失败。"); }
      finally { busy = false; upload.disabled = false; picker.value = ""; }
    }
    picker.addEventListener("change", function () { uploadImage(picker.files[0]); });
    input.addEventListener("paste", function (event) {
      var files = event.clipboardData && event.clipboardData.files;
      if (files && files.length && /^image\//.test(files[0].type)) { event.preventDefault(); uploadImage(files[0]); }
    });
    function travel(delta) {
      var next = cursor + delta; if (next < 0 || next >= history.length) return;
      cursor = next; replaying = true; input.value = history[cursor]; input.dispatchEvent(new Event("input", {bubbles:true})); replaying = false; input.focus();
    }
    var undo = button("撤销", "↶", function () { travel(-1); });
    var redo = button("重做", "↷", function () { travel(1); });
    async function renderPreview() {
      if (mode === "edit") return;
      if (size(input.value) > 102400) { report("每段 Markdown 内容不能超过 100 KB。"); return; }
      var current = ++revision; if (controller) controller.abort(); controller = new AbortController();
      report("正在生成预览…");
      try {
        var response = await fetch(prefix + "/admin/markdown/preview", {method:"POST", credentials:"same-origin", signal:controller.signal, headers:{"X-CSRF-Token":input.form.querySelector('[name=csrf]').value,"Accept":"application/json"},body:new URLSearchParams({source:input.value})});
        if (response.redirected) throw new Error(t("登录已过期，请先保存文案，再重新登录。"));
        var data = await response.json(); if (!response.ok) throw new Error((data.error || {}).message || t("预览失败，请稍后重试。"));
        if (current !== revision) return;
        // Only the server's sanitized Markdown is inserted as HTML.
        output.innerHTML = data.html;
        if (!output.textContent.trim() && !output.querySelector("img,hr")) output.textContent = t("暂无可预览内容。");
        report("预览已更新，修改尚未保存。");
      } catch (error) { if (current === revision && error.name !== "AbortError") report(error.message || "预览失败，请稍后重试。"); }
    }
    var modes = document.createElement("div"); modes.className = "md-modes"; editor.querySelector(".markdown-toolbar").append(modes);
    var modeButtons = {};
    function setMode(value) {
      if (value === "preview" && size(input.value) > 102400) { report("每段 Markdown 内容不能超过 100 KB。"); return; }
      mode = value; editor.dataset.mode = mode; input.hidden = mode === "preview"; output.hidden = mode === "edit"; toolbar.hidden = mode === "preview";
      preview.setAttribute("aria-expanded", String(mode !== "edit"));
      Object.keys(modeButtons).forEach(function (key) { modeButtons[key].setAttribute("aria-pressed", String(key === mode)); });
      if (mode !== "edit") renderPreview(); else { ++revision; if (controller) controller.abort(); report(""); }
    }
    [["edit", "编辑"], ["split", "边写边看"], ["preview", "预览"]].forEach(function (item) {
      var b = document.createElement("button"); b.type = "button"; b.className = "btn sm"; b.textContent = t(item[1]); b.dataset.mdMode = item[0]; b.setAttribute("data-admin-i18n", ""); b.addEventListener("click", function () { setMode(item[0]); }); modes.append(b); modeButtons[item[0]] = b;
    });
    preview.addEventListener("click", function () { setMode("split"); });
    input.addEventListener("input", function () { ++revision; if (controller) controller.abort(); clearTimeout(timer); remember(); refreshCounter(); if (mode !== "edit") timer = setTimeout(renderPreview, 550); });
    input.addEventListener("keydown", function (event) {
      if (event.isComposing || !(event.ctrlKey || event.metaKey)) return;
      var key = event.key.toLowerCase();
      if (["b", "i", "k", "z", "y"].indexOf(key) < 0) return;
      event.preventDefault(); event.stopPropagation();
      if (key === "b") wrap("**", "**", "重点内容");
      if (key === "i") wrap("*", "*", "补充说明");
      if (key === "k") linkDialog();
      if (key === "z") travel(event.shiftKey ? 1 : -1);
      if (key === "y") travel(1);
    });
    input.form.addEventListener("submit", function (event) { if (busy) { event.preventDefault(); report("图片仍在上传，请稍候再保存。"); } });
    document.addEventListener("cw-admin-languagechange", function () {
      toolbar.setAttribute("aria-label", t("Markdown 格式工具"));
      toolbar.querySelectorAll("[data-md-label]").forEach(function (b) { var old = b.title; b.title = t(b.dataset.mdLabel); b.setAttribute("aria-label", b.title); if (b.textContent === old) b.textContent = b.title; }); refreshCounter();
    });
    remember(); refreshCounter(); setMode("edit");
  });
})();
