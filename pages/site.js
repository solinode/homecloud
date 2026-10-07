(function () {
  "use strict";

  // Install commands point at this site's /scripts/ when served over http(s),
  // so preview deployments install from themselves. The HTML already has the
  // public URL for when JavaScript is off.
  if (location.protocol.indexOf("http") === 0) {
    var base = location.origin + "/scripts/";
    document.querySelectorAll("[data-script]").forEach(function (el) {
      el.textContent = el.dataset.script === "ps1"
        ? "irm " + base + "install.ps1 | iex"
        : "curl -fsSL " + base + "install.sh | sh";
    });
  }

  // Tabs (install methods, CI integrations).
  document.querySelectorAll("[role=tablist]").forEach(function (list) {
    var tabs = Array.prototype.slice.call(list.querySelectorAll("[role=tab]"));
    function select(tab, focus) {
      tabs.forEach(function (t) {
        var on = t === tab;
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.tabIndex = on ? 0 : -1;
        document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
      });
      if (focus) tab.focus();
    }
    tabs.forEach(function (tab, i) {
      tab.addEventListener("click", function () { select(tab, false); });
      tab.addEventListener("keydown", function (e) {
        var n = null;
        if (e.key === "ArrowRight" || e.key === "ArrowDown") n = tabs[(i + 1) % tabs.length];
        if (e.key === "ArrowLeft" || e.key === "ArrowUp") n = tabs[(i - 1 + tabs.length) % tabs.length];
        if (e.key === "Home") n = tabs[0];
        if (e.key === "End") n = tabs[tabs.length - 1];
        if (n) { e.preventDefault(); select(n, true); }
      });
    });
  });

  // Copy buttons.
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var text = document.getElementById(btn.dataset.copy).textContent.trim();
      var done = function (label) {
        btn.textContent = label;
        setTimeout(function () { btn.textContent = "Copy"; }, 1600);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(function () { done("Copied"); }, function () { done("Failed"); });
      } else {
        var range = document.createRange();
        range.selectNodeContents(document.getElementById(btn.dataset.copy));
        var sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
        done("Press ⌘C");
      }
    });
  });

  // Theme toggle. The choice is remembered on this device only.
  var btn = document.getElementById("theme");
  if (btn) {
    btn.addEventListener("click", function () {
      var root = document.documentElement;
      var cur = root.dataset.theme ||
        (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
      var next = cur === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      btn.setAttribute("aria-label", next === "dark" ? "Switch to light theme" : "Switch to dark theme");
      try { localStorage.setItem("theme", next); } catch (e) {}
    });
  }
})();
