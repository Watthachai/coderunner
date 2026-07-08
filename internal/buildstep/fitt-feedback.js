/*!
 * fitt-feedback — the in-demo feedback widget CRN injects into every build.
 * Self-contained, framework-agnostic, Shadow-DOM isolated. Lets a demo tester
 * pin elements on the live page (CSS selector + bounding box + a foreignObject
 * "screenshot"), pick a category/priority, describe the change, and POST it to
 * the PostgREST ingest — which lands in CRN's Edit Request Panel.
 *
 * Config comes from this script tag's dataset:
 *   <script data-project="<uuid>" data-ingest="https://host/feedback_requests"> ...
 */
(function () {
  "use strict";

  var script = document.currentScript;
  var cfg = (script && script.dataset) || {};
  var PROJECT = cfg.project || "";
  var INGEST = cfg.ingest || "";
  if (!INGEST) {
    console.warn("[fitt-feedback] no data-ingest URL — widget disabled");
    return;
  }

  var GREEN = "#58cc02";
  var pins = []; // { selector, label, note, box, region_shot }
  var pointing = false;
  var category = "feature";
  var priority = "med";

  // --- element capture --------------------------------------------------------

  function cssEscape(s) {
    return window.CSS && CSS.escape ? CSS.escape(s) : String(s).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  }

  // A reasonably stable CSS selector path for an element (id short-circuits).
  function cssPath(el) {
    if (!el || el.nodeType !== 1) return "";
    var path = [];
    while (el && el.nodeType === 1 && path.length < 6) {
      var sel = el.nodeName.toLowerCase();
      if (el.id) {
        path.unshift(sel + "#" + cssEscape(el.id));
        break;
      }
      if (el.classList && el.classList.length) {
        sel += "." + cssEscape(el.classList[0]);
      }
      var parent = el.parentNode;
      if (parent && parent.children) {
        var same = Array.prototype.filter.call(parent.children, function (c) {
          return c.nodeName === el.nodeName;
        });
        if (same.length > 1) {
          sel += ":nth-of-type(" + (Array.prototype.indexOf.call(same, el) + 1) + ")";
        }
      }
      path.unshift(sel);
      el = parent;
    }
    return path.join(" > ");
  }

  function shortLabel(el) {
    var t = el.nodeName.toLowerCase();
    if (el.id) return t + "#" + el.id;
    if (el.classList && el.classList.length) return t + "." + el.classList[0];
    var txt = (el.textContent || "").trim().replace(/\s+/g, " ");
    return txt ? t + ' "' + txt.slice(0, 24) + '"' : t;
  }

  // Copy computed styles onto a clone tree so an SVG <foreignObject> renders it.
  function inlineStyles(src, dst) {
    var cs = getComputedStyle(src);
    var s = "";
    for (var i = 0; i < cs.length; i++) {
      var p = cs[i];
      s += p + ":" + cs.getPropertyValue(p) + ";";
    }
    dst.setAttribute("style", s);
    var sc = src.children || [];
    var dc = dst.children || [];
    for (var j = 0; j < sc.length && j < dc.length; j++) inlineStyles(sc[j], dc[j]);
  }

  // Best-effort "screenshot" of one element as an SVG data URI (self-contained;
  // cross-origin images/fonts may not render, which is acceptable).
  function captureElement(el) {
    try {
      var rect = el.getBoundingClientRect();
      var w = Math.max(1, Math.ceil(rect.width));
      var h = Math.max(1, Math.ceil(rect.height));
      var clone = el.cloneNode(true);
      inlineStyles(el, clone);
      clone.style.margin = "0";
      var html = new XMLSerializer().serializeToString(clone);
      var svg =
        "<svg xmlns='http://www.w3.org/2000/svg' width='" + w + "' height='" + h + "'>" +
        "<foreignObject width='100%' height='100%'>" +
        "<div xmlns='http://www.w3.org/1999/xhtml'>" + html + "</div>" +
        "</foreignObject></svg>";
      return "data:image/svg+xml;base64," + btoa(unescape(encodeURIComponent(svg)));
    } catch (e) {
      return "";
    }
  }

  function pageBox(el) {
    var r = el.getBoundingClientRect();
    return {
      x: Math.round(r.left + window.scrollX),
      y: Math.round(r.top + window.scrollY),
      w: Math.round(r.width),
      h: Math.round(r.height),
    };
  }

  // --- UI (Shadow DOM) --------------------------------------------------------

  var host = document.createElement("div");
  host.id = "fitt-feedback-host";
  var root = host.attachShadow({ mode: "open" });
  (document.body || document.documentElement).appendChild(host);

  // A page-level highlight box used during point mode (outside shadow so it can
  // sit over page content at page coordinates).
  var hl = document.createElement("div");
  hl.style.cssText =
    "position:fixed;z-index:2147483646;pointer-events:none;border:2px solid " +
    GREEN + ";background:rgba(88,204,2,0.12);border-radius:4px;display:none;";
  document.documentElement.appendChild(hl);

  root.innerHTML =
    "<style>" +
    ":host{all:initial}" +
    "*{box-sizing:border-box;font-family:ui-sans-serif,system-ui,-apple-system,'Segoe UI',sans-serif}" +
    ".fab{position:fixed;right:20px;bottom:20px;z-index:2147483647;width:56px;height:56px;border-radius:50%;" +
    "background:" + GREEN + ";color:#fff;border:none;box-shadow:0 4px 0 #61b800,0 8px 24px rgba(0,0,0,.25);" +
    "font-size:24px;cursor:pointer;transition:transform .1s}" +
    ".fab:active{transform:translateY(4px);box-shadow:0 0 0 #61b800,0 4px 16px rgba(0,0,0,.25)}" +
    ".panel{position:fixed;right:20px;bottom:88px;z-index:2147483647;width:340px;max-width:calc(100vw - 40px);" +
    "max-height:calc(100vh - 120px);overflow:auto;background:#fff;color:#3c3c3c;border:2px solid #e5e5e5;" +
    "border-radius:16px;box-shadow:0 16px 48px rgba(0,0,0,.28);padding:16px}" +
    ".panel[hidden]{display:none}" +
    "h3{margin:0 0 10px;font-size:15px;font-weight:800;color:#3c3c3c}" +
    ".row{display:flex;gap:6px;flex-wrap:wrap;margin-bottom:10px}" +
    ".chip{flex:1;min-width:60px;padding:7px 4px;border:2px solid #e5e5e5;border-radius:10px;background:#fff;" +
    "font-size:12px;font-weight:800;cursor:pointer;color:#777;text-align:center}" +
    ".chip.on{border-color:" + GREEN + ";color:" + GREEN + ";background:rgba(88,204,2,.08)}" +
    ".chip.on.prio{border-color:#1cb0f6;color:#1cb0f6;background:rgba(28,176,246,.08)}" +
    "label{font-size:11px;text-transform:uppercase;letter-spacing:.05em;color:#afafaf;font-weight:800;display:block;margin:2px 0 6px}" +
    "textarea{width:100%;min-height:64px;border:2px solid #e5e5e5;border-radius:10px;padding:8px;font-size:13px;" +
    "resize:vertical;font-family:inherit;color:#3c3c3c}" +
    "textarea:focus,input:focus{outline:none;border-color:" + GREEN + "}" +
    ".pinbtn{width:100%;padding:9px;border:2px dashed #cfcfcf;border-radius:10px;background:#fafafa;color:#777;" +
    "font-size:13px;font-weight:800;cursor:pointer;margin-bottom:10px}" +
    ".pinbtn.on{border-color:" + GREEN + ";color:" + GREEN + ";border-style:solid;background:rgba(88,204,2,.06)}" +
    ".pins{list-style:none;margin:0 0 10px;padding:0;display:flex;flex-direction:column;gap:6px}" +
    ".pin{display:flex;gap:8px;align-items:flex-start;border:1px solid #eee;border-radius:8px;padding:6px}" +
    ".pin img{width:64px;height:auto;max-height:48px;border-radius:4px;border:1px solid #eee;object-fit:cover}" +
    ".pin .pb{flex:1;min-width:0}" +
    ".pin code{font-size:10px;color:" + GREEN + ";display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}" +
    ".pin input{width:100%;border:1px solid #e5e5e5;border-radius:6px;padding:4px 6px;font-size:12px;margin-top:3px}" +
    ".pin .x{border:none;background:none;color:#ff4b4b;cursor:pointer;font-size:14px;font-weight:800;padding:0 2px}" +
    ".send{width:100%;padding:11px;border:none;border-radius:12px;background:" + GREEN + ";color:#fff;" +
    "font-size:14px;font-weight:800;cursor:pointer;box-shadow:0 4px 0 #61b800}" +
    ".send:active{transform:translateY(4px);box-shadow:0 0 0 #61b800}" +
    ".send:disabled{opacity:.6;cursor:default}" +
    ".status{font-size:12px;text-align:center;margin-top:8px;color:#777;min-height:16px}" +
    ".status.ok{color:" + GREEN + "}.status.err{color:#ff4b4b}" +
    "</style>" +
    "<button class='fab' title='Send feedback'>💬</button>" +
    "<div class='panel' hidden>" +
    "<h3>💬 บอกสิ่งที่อยากแก้</h3>" +
    "<label>ประเภท</label><div class='row cat'>" +
    "<button class='chip' data-cat='bug'>🐞 bug</button>" +
    "<button class='chip on' data-cat='feature'>✨ feature</button>" +
    "<button class='chip' data-cat='style'>🎨 style</button></div>" +
    "<label>ความสำคัญ</label><div class='row prio'>" +
    "<button class='chip prio' data-prio='low'>low</button>" +
    "<button class='chip prio on' data-prio='med'>med</button>" +
    "<button class='chip prio' data-prio='high'>high</button></div>" +
    "<button class='pinbtn'>📍 ปักจุดบนหน้า</button>" +
    "<ul class='pins'></ul>" +
    "<label>รายละเอียด</label>" +
    "<textarea placeholder='อยากให้เปลี่ยนเป็นแบบไหน?'></textarea>" +
    "<button class='send'>ส่ง feedback</button>" +
    "<div class='status'></div>" +
    "</div>";

  var fab = root.querySelector(".fab");
  var panel = root.querySelector(".panel");
  var pinbtn = root.querySelector(".pinbtn");
  var pinsEl = root.querySelector(".pins");
  var noteEl = root.querySelector("textarea");
  var sendBtn = root.querySelector(".send");
  var statusEl = root.querySelector(".status");

  fab.addEventListener("click", function () {
    panel.hidden = !panel.hidden;
    if (panel.hidden) stopPointing();
  });

  root.querySelectorAll(".cat .chip").forEach(function (c) {
    c.addEventListener("click", function () {
      root.querySelectorAll(".cat .chip").forEach(function (x) { x.classList.remove("on"); });
      c.classList.add("on");
      category = c.getAttribute("data-cat");
    });
  });
  root.querySelectorAll(".prio .chip").forEach(function (c) {
    c.addEventListener("click", function () {
      root.querySelectorAll(".prio .chip").forEach(function (x) { x.classList.remove("on"); });
      c.classList.add("on");
      priority = c.getAttribute("data-prio");
    });
  });

  pinbtn.addEventListener("click", function () {
    if (pointing) stopPointing();
    else startPointing();
  });

  function renderPins() {
    pinsEl.innerHTML = "";
    pins.forEach(function (p, i) {
      var li = document.createElement("li");
      li.className = "pin";
      var img = p.region_shot ? "<img src='" + p.region_shot + "' alt=''>" : "";
      li.innerHTML =
        img +
        "<div class='pb'><code>" + p.selector + "</code>" +
        "<input placeholder='โน้ตจุดนี้…' value='" + (p.note || "").replace(/'/g, "&#39;") + "'></div>" +
        "<button class='x' title='remove'>✕</button>";
      li.querySelector("input").addEventListener("input", function (e) { pins[i].note = e.target.value; });
      li.querySelector(".x").addEventListener("click", function () { pins.splice(i, 1); renderPins(); });
      pinsEl.appendChild(li);
    });
  }

  // --- point mode -------------------------------------------------------------

  function onMove(e) {
    var el = e.target;
    if (!el || host.contains(el) || el === hl) return;
    var r = el.getBoundingClientRect();
    hl.style.display = "block";
    hl.style.left = r.left + "px";
    hl.style.top = r.top + "px";
    hl.style.width = r.width + "px";
    hl.style.height = r.height + "px";
  }

  function onPick(e) {
    var el = e.target;
    if (!el || host.contains(el)) return; // clicks on our UI pass through
    e.preventDefault();
    e.stopPropagation();
    pins.push({
      selector: cssPath(el),
      label: shortLabel(el),
      note: "",
      box: pageBox(el),
      region_shot: captureElement(el),
    });
    renderPins();
    stopPointing();
    panel.hidden = false;
  }

  function startPointing() {
    pointing = true;
    pinbtn.classList.add("on");
    pinbtn.textContent = "📍 คลิก element ที่ต้องการ (Esc ยกเลิก)";
    panel.hidden = true;
    document.addEventListener("mousemove", onMove, true);
    document.addEventListener("click", onPick, true);
    document.addEventListener("keydown", onEsc, true);
  }

  function stopPointing() {
    pointing = false;
    pinbtn.classList.remove("on");
    pinbtn.textContent = "📍 ปักจุดบนหน้า";
    hl.style.display = "none";
    document.removeEventListener("mousemove", onMove, true);
    document.removeEventListener("click", onPick, true);
    document.removeEventListener("keydown", onEsc, true);
  }

  function onEsc(e) {
    if (e.key === "Escape") { stopPointing(); panel.hidden = false; }
  }

  // --- submit -----------------------------------------------------------------

  sendBtn.addEventListener("click", function () {
    var note = (noteEl.value || "").trim();
    if (!note && pins.length === 0) {
      setStatus("เพิ่มโน้ตหรือปักจุดก่อนนะ", "err");
      return;
    }
    sendBtn.disabled = true;
    setStatus("กำลังส่ง…", "");
    var body = {
      project_id: PROJECT,
      category: category,
      priority: priority,
      note: note,
      page_url: location.href,
      reporter: "",
      payload: {
        pins: pins,
        full_shot: "",
        viewport: { w: window.innerWidth, h: window.innerHeight },
        user_agent: navigator.userAgent,
      },
    };
    fetch(INGEST, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        setStatus("ส่งแล้ว ขอบคุณ! ✓", "ok");
        pins = []; renderPins(); noteEl.value = "";
        setTimeout(function () { panel.hidden = true; setStatus("", ""); }, 1400);
      })
      .catch(function (err) {
        setStatus("ส่งไม่สำเร็จ — " + err.message, "err");
      })
      .finally(function () { sendBtn.disabled = false; });
  });

  function setStatus(msg, kind) {
    statusEl.textContent = msg;
    statusEl.className = "status" + (kind ? " " + kind : "");
  }
})();
