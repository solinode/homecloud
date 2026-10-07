(function () {
  "use strict";
  var $$ = function (sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  // Install commands point at this site's /scripts/ when served over http(s),
  // so preview deployments install from themselves. The HTML already has the
  // public URL for when JavaScript is off.
  if (location.protocol.indexOf("http") === 0) {
    var base = location.origin + "/scripts/";
    $$("[data-script]").forEach(function (el) {
      el.textContent = el.dataset.script === "ps1"
        ? "irm " + base + "install.ps1 | iex"
        : "curl -fsSL " + base + "install.sh | sh";
    });
  }

  // Tabs (install methods, CI integrations, console screens).
  var galUrl = document.getElementById("gal-url");
  $$("[role=tablist]").forEach(function (list) {
    var tabs = $$("[role=tab]", list);
    var vertical = list.getAttribute("aria-orientation") === "vertical";
    function select(tab, focus) {
      tabs.forEach(function (t) {
        var on = t === tab;
        var panel = document.getElementById(t.getAttribute("aria-controls"));
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.tabIndex = on ? 0 : -1;
        panel.hidden = !on;
        if (on && panel.dataset.url && galUrl) galUrl.textContent = panel.dataset.url;
      });
      if (focus) tab.focus();
    }
    tabs.forEach(function (tab, i) {
      tab.addEventListener("click", function () { select(tab, false); });
      tab.addEventListener("keydown", function (e) {
        var next = vertical ? ["ArrowDown", "ArrowRight"] : ["ArrowRight", "ArrowDown"];
        var prev = vertical ? ["ArrowUp", "ArrowLeft"] : ["ArrowLeft", "ArrowUp"];
        var n = null;
        if (next.indexOf(e.key) >= 0) n = tabs[(i + 1) % tabs.length];
        if (prev.indexOf(e.key) >= 0) n = tabs[(i - 1 + tabs.length) % tabs.length];
        if (e.key === "Home") n = tabs[0];
        if (e.key === "End") n = tabs[tabs.length - 1];
        if (n) { e.preventDefault(); select(n, true); }
      });
    });
  });

  // Copy buttons, with a check mark and a screen reader announcement.
  var status = document.getElementById("copy-status");
  $$("[data-copy]").forEach(function (btn) {
    var timer;
    btn.addEventListener("click", function () {
      var src = document.getElementById(btn.dataset.copy);
      var text = src.textContent.trim();
      var done = function (msg) {
        btn.classList.add("done");
        if (status) status.textContent = msg;
        clearTimeout(timer);
        timer = setTimeout(function () { btn.classList.remove("done"); if (status) status.textContent = ""; }, 1800);
      };
      if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(text).then(function () { done("Copied to clipboard"); }, function () { select(src); });
      } else {
        select(src);
      }
      function select(el) {
        var range = document.createRange();
        range.selectNodeContents(el);
        var sel = getSelection(); sel.removeAllRanges(); sel.addRange(range);
        if (status) status.textContent = "Command selected. Press Ctrl+C or Cmd+C to copy.";
      }
    });
  });

  // Theme toggle. The choice is remembered on this device only.
  var themeBtn = document.getElementById("theme");
  if (themeBtn) {
    themeBtn.addEventListener("click", function () {
      var root = document.documentElement;
      var cur = root.dataset.theme || (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
      var next = cur === "dark" ? "light" : "dark";
      root.dataset.theme = next;
      themeBtn.setAttribute("aria-label", next === "dark" ? "Switch to light theme" : "Switch to dark theme");
      try { localStorage.setItem("theme", next); } catch (e) {}
    });
  }

  // Mobile menu.
  var menu = document.getElementById("menu");
  var nav = document.getElementById("site-nav");
  if (menu && nav) {
    var setOpen = function (open) {
      nav.classList.toggle("open", open);
      menu.setAttribute("aria-expanded", open ? "true" : "false");
      menu.setAttribute("aria-label", open ? "Close menu" : "Open menu");
    };
    menu.addEventListener("click", function () { setOpen(!nav.classList.contains("open")); });
    $$("a", nav).forEach(function (a) { a.addEventListener("click", function () { setOpen(false); }); });
    document.addEventListener("keydown", function (e) { if (e.key === "Escape" && nav.classList.contains("open")) { setOpen(false); menu.focus(); } });
  }

  // Reveal on scroll.
  var items = $$(".reveal");
  if ("IntersectionObserver" in window && !matchMedia("(prefers-reduced-motion: reduce)").matches) {
    var io = new IntersectionObserver(function (entries) {
      entries.forEach(function (e) {
        if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); }
      });
    }, { rootMargin: "0px 0px -8% 0px", threshold: 0.08 });
    items.forEach(function (el) { io.observe(el); });
  } else {
    items.forEach(function (el) { el.classList.add("in"); });
  }
})();
