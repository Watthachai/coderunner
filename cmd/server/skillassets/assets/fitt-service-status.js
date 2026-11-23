// FITT service-status banner — shipped by Code Runner. Copy it unchanged to public/; do not edit.
// While FITT Billing has the app in read-only mode, every page shows the message at the top,
// so people know why nothing saves. It asks the app's own GET /api/service-status when the
// page loads and every 5 minutes; when it can't get an answer it leaves the page as it is.
// It fills the root layout's <div id="fitt-service-status" hidden suppressHydrationWarning>
// and adds nothing to the page itself: a node put into <body> before React hydrates makes
// React throw the page away and render it again (23 Nov 2026, Next.js 16).
(function () {
  var ID = "fitt-service-status";
  function show(message) {
    var el = document.getElementById(ID);
    if (!el) return;
    var s = el.style;
    s.position = "sticky";
    s.top = "0";
    s.zIndex = "2147483000";
    s.padding = "10px 16px";
    s.background = "#fff4d6";
    s.color = "#5c3d00";
    s.borderBottom = "1px solid #e8c766";
    s.font = "14px/1.5 system-ui, sans-serif";
    s.textAlign = "center";
    el.setAttribute("role", "status");
    el.textContent = message;
    el.hidden = false;
  }
  function hide() {
    var el = document.getElementById(ID);
    if (!el) return;
    el.hidden = true;
    el.textContent = "";
  }
  function check() {
    fetch("/api/service-status", { cache: "no-store", credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (s) {
        if (!s) return;
        if (s.status === "read_only") show(s.message);
        else hide();
      })
      .catch(function () {});
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", check);
  else check();
  setInterval(check, 300000);
})();
