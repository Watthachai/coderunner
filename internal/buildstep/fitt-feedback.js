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

  // document.currentScript is set for a parser-inserted <script> (the inline
  // HTML case), but is null when a framework (React/Next) inserts the external
  // <script> itself — fall back to finding our tag by its marker attribute.
  var script =
    document.currentScript ||
    document.querySelector("script[data-fitt-feedback]");
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
  var cropOverlay = null,
    cropRect = null,
    cropStart = null;
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

  // Rasterize an already-loaded <img> to a (downscaled) data URI. An SVG loaded
  // as an <img> runs in restricted mode and cannot fetch external image URLs, so
  // a cloned <img src="https://…"> renders blank — the "placeholder div" look.
  // Painting the live element to a canvas embeds its actual pixels instead.
  // Returns null when the image isn't loaded yet or is a cross-origin canvas
  // taint (no client-side capture can read those — a browser security limit).
  function imgToDataURL(img) {
    try {
      var nw = img.naturalWidth,
        nh = img.naturalHeight;
      if (!img.complete || !nw || !nh) return null;
      var MAX = 1000; // hard cap on the longest side
      // Capture at the element's DISPLAYED size (never upscale), so a thumbnail
      // isn't stored at full natural resolution.
      var rect = img.getBoundingClientRect();
      var target = Math.min(MAX, Math.max(rect.width, rect.height) || Math.max(nw, nh));
      var scale = Math.min(1, target / Math.max(nw, nh));
      var cw = Math.max(1, Math.round(nw * scale));
      var ch = Math.max(1, Math.round(nh * scale));
      var c = document.createElement("canvas");
      c.width = cw;
      c.height = ch;
      c.getContext("2d").drawImage(img, 0, 0, cw, ch);
      return c.toDataURL("image/png"); // throws if the canvas is CORS-tainted
    } catch (e) {
      return null;
    }
  }

  function rectsIntersect(r, clip) {
    return r.right > clip.left && r.left < clip.right && r.bottom > clip.top && r.top < clip.bottom;
  }

  // Copy computed styles onto a clone tree so an SVG <foreignObject> renders it.
  // <img> elements are rasterized to data URIs (see imgToDataURL); an optional
  // clip rect (viewport coords) limits rasterization to images inside it, so a
  // small region crop doesn't embed every image on the page. background-image
  // URLs still won't load (that would need async fetching).
  function inlineStyles(src, dst, clip) {
    var cs = getComputedStyle(src);
    var s = "";
    for (var i = 0; i < cs.length; i++) {
      var p = cs[i];
      s += p + ":" + cs.getPropertyValue(p) + ";";
    }
    dst.setAttribute("style", s);
    if (
      src.tagName === "IMG" &&
      dst.tagName === "IMG" &&
      (!clip || rectsIntersect(src.getBoundingClientRect(), clip))
    ) {
      var uri = imgToDataURL(src);
      if (uri) {
        dst.setAttribute("src", uri);
        dst.removeAttribute("srcset"); // else the browser may re-pick the URL
      }
    }
    var sc = src.children || [];
    var dc = dst.children || [];
    for (var j = 0; j < sc.length && j < dc.length; j++) inlineStyles(sc[j], dc[j], clip);
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
      return "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
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
    ".cropbtn{width:100%;padding:9px;border:2px dashed #cfcfcf;border-radius:10px;background:#fafafa;color:#777;" +
    "font-size:13px;font-weight:800;cursor:pointer;margin-bottom:10px}" +
    ".cropbtn.on{border-color:" + GREEN + ";color:" + GREEN + ";border-style:solid;background:rgba(88,204,2,.06)}" +
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
    "<button class='cropbtn'>◻ ลากครอปพื้นที่</button>" +
    "<ul class='pins'></ul>" +
    "<label>รายละเอียด</label>" +
    "<textarea placeholder='อยากให้เปลี่ยนเป็นแบบไหน?'></textarea>" +
    "<button class='send'>ส่ง feedback</button>" +
    "<div class='status'></div>" +
    "</div>";

  var fab = root.querySelector(".fab");
  var panel = root.querySelector(".panel");
  var pinbtn = root.querySelector(".pinbtn");
  var cropbtn = root.querySelector(".cropbtn");
  var pinsEl = root.querySelector(".pins");
  var noteEl = root.querySelector("textarea");
  var sendBtn = root.querySelector(".send");
  var statusEl = root.querySelector(".status");

  fab.addEventListener("click", function () {
    panel.hidden = !panel.hidden;
    if (panel.hidden) {
      stopPointing();
      stopCrop();
    }
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

  cropbtn.addEventListener("click", function () {
    if (cropOverlay) stopCrop();
    else startCrop();
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
    stopCrop();
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

  // --- region crop (drag a rectangle to capture an area) ----------------------

  function startCrop() {
    stopPointing();
    cropbtn.classList.add("on");
    cropbtn.textContent = "◻ ลากคลุมพื้นที่ (Esc ยกเลิก)";
    panel.hidden = true;
    cropOverlay = document.createElement("div");
    cropOverlay.style.cssText =
      "position:fixed;inset:0;z-index:2147483646;cursor:crosshair;background:rgba(0,0,0,0.06)";
    cropRect = document.createElement("div");
    cropRect.style.cssText =
      "position:fixed;display:none;pointer-events:none;border:2px solid " +
      GREEN +
      ";background:rgba(88,204,2,0.12)";
    cropOverlay.appendChild(cropRect);
    document.documentElement.appendChild(cropOverlay);
    cropOverlay.addEventListener("mousedown", onCropDown);
    document.addEventListener("keydown", onCropEsc, true);
  }

  function stopCrop() {
    cropbtn.classList.remove("on");
    cropbtn.textContent = "◻ ลากครอปพื้นที่";
    document.removeEventListener("keydown", onCropEsc, true);
    document.removeEventListener("mousemove", onCropMove, true);
    document.removeEventListener("mouseup", onCropUp, true);
    if (cropOverlay) {
      cropOverlay.remove();
      cropOverlay = null;
      cropRect = null;
    }
    cropStart = null;
  }

  function onCropEsc(e) {
    if (e.key === "Escape") { stopCrop(); panel.hidden = false; }
  }

  function drawCropRect(e) {
    var x = Math.min(cropStart.x, e.clientX),
      y = Math.min(cropStart.y, e.clientY);
    var w = Math.abs(e.clientX - cropStart.x),
      h = Math.abs(e.clientY - cropStart.y);
    cropRect.style.left = x + "px";
    cropRect.style.top = y + "px";
    cropRect.style.width = w + "px";
    cropRect.style.height = h + "px";
  }

  function onCropDown(e) {
    e.preventDefault();
    cropStart = { x: e.clientX, y: e.clientY };
    cropRect.style.display = "block";
    drawCropRect(e);
    document.addEventListener("mousemove", onCropMove, true);
    document.addEventListener("mouseup", onCropUp, true);
  }

  function onCropMove(e) {
    if (cropStart) drawCropRect(e);
  }

  function onCropUp(e) {
    if (!cropStart) return;
    var x = Math.min(cropStart.x, e.clientX),
      y = Math.min(cropStart.y, e.clientY);
    var w = Math.abs(e.clientX - cropStart.x),
      h = Math.abs(e.clientY - cropStart.y);
    stopCrop();
    panel.hidden = false;
    if (w < 8 || h < 8) return; // ignore tiny drags
    pins.push({
      selector: "region",
      label: "พื้นที่ " + Math.round(w) + "×" + Math.round(h),
      note: "",
      box: {
        x: Math.round(x + window.scrollX),
        y: Math.round(y + window.scrollY),
        w: Math.round(w),
        h: Math.round(h),
      },
      region_shot: captureRegion(x, y, w, h),
    });
    renderPins();
  }

  // Capture a viewport rectangle by cloning <body> into an SVG foreignObject,
  // offset so the selected rect aligns to the image origin. Cross-origin images
  // won't render (CORS) but layout/text/colors do — enough to point at a spot.
  function captureRegion(vx, vy, w, h) {
    try {
      w = Math.max(1, Math.round(w));
      h = Math.max(1, Math.round(h));
      var body = document.body;
      var clone = body.cloneNode(true);
      // Only rasterize images inside the crop rectangle (viewport coords), so a
      // small crop over an image-heavy page stays small instead of embedding
      // every full-res image on the page.
      inlineStyles(body, clone, { left: vx, top: vy, right: vx + w, bottom: vy + h });
      clone.style.margin = "0";
      var html = new XMLSerializer().serializeToString(clone);
      var fw = Math.max(body.scrollWidth, window.innerWidth);
      var fh = Math.max(body.scrollHeight, window.innerHeight);
      var ox = -(vx + window.scrollX),
        oy = -(vy + window.scrollY);
      var svg =
        "<svg xmlns='http://www.w3.org/2000/svg' width='" + w + "' height='" + h + "'>" +
        "<foreignObject x='" + ox + "' y='" + oy + "' width='" + fw + "' height='" + fh + "'>" +
        "<div xmlns='http://www.w3.org/1999/xhtml'>" + html + "</div>" +
        "</foreignObject></svg>";
      return "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg);
    } catch (e) {
      return "";
    }
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
