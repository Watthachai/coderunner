/*!
 * fitt-feedback — the in-demo feedback widget CRN injects into every build.
 * Self-contained, framework-agnostic, Shadow-DOM isolated. Lets a demo tester
 * pin elements on the live page (CSS selector + bounding box + a PNG
 * "screenshot"), pick a category/priority, describe the change, and POST it to
 * data-ingest: a Next app's own /api/fitt-feedback route, which sends it on to
 * FITT_FEEDBACK_URL, or for a static page the receiver itself.
 *
 * Config comes from this script tag's dataset:
 *   <script data-project="<uuid>" data-ingest="/api/fitt-feedback" data-tickets="/api/fitt-feedback"> ...
 *
 * data-tickets adds the 📋 window: the customer logs in to fittcore, sees this job's
 * tickets and sends the ones they pick on to DV's fittcore (the central base), as
 * fittcore's own page does. The route passes on the customer's token; see feedback.go.
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
  // The 📋 ticket window, through the app's own route (a Next app's /api/fitt-feedback);
  // a static page has no server to call fittcore for it, so it goes without.
  var TICKETS = cfg.tickets || "";
  if (!INGEST) {
    console.warn("[fitt-feedback] no data-ingest URL — widget disabled");
    return;
  }

  var GREEN = "#58cc02";
  var pins = []; // { selector, label, note, box, region_shot }
  var shots = []; // pin pictures still being painted; sending waits for them
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

  // Fittcore takes PNG, JPEG or WebP only, so a snapshot (SVG, foreignObject) is
  // painted onto a canvas and sent as PNG, its longest side at most SHOT_MAX px.
  // Resolves "" when the browser will not paint it (a decode error or a tainted
  // canvas): the pin goes without a picture, never with the SVG.
  var SHOT_MAX = 1600;
  function svgToPNG(svgURI, w, h) {
    return new Promise(function (resolve) {
      if (!svgURI) return resolve("");
      var img = new Image();
      img.onload = function () {
        try {
          var scale = Math.min(1, SHOT_MAX / Math.max(w, h));
          var c = document.createElement("canvas");
          c.width = Math.max(1, Math.round(w * scale));
          c.height = Math.max(1, Math.round(h * scale));
          var ctx = c.getContext("2d");
          ctx.fillStyle = "#fff"; // a transparent page reads as black in most viewers
          ctx.fillRect(0, 0, c.width, c.height);
          ctx.drawImage(img, 0, 0, c.width, c.height);
          resolve(c.toDataURL("image/png"));
        } catch (e) {
          resolve("");
        }
      };
      img.onerror = function () { resolve(""); };
      img.src = svgURI;
    });
  }

  // Best-effort "screenshot" of one element, resolving to a PNG data URI (see
  // svgToPNG; cross-origin images/fonts may not render, which is acceptable). The
  // element is copied now; only the painting happens later.
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
      return svgToPNG("data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg), w, h);
    } catch (e) {
      return Promise.resolve("");
    }
  }

  // addPin lists pin now and fills in its picture once shot is painted.
  function addPin(pin, shot) {
    pins.push(pin);
    shots.push(shot.then(function (png) {
      pin.region_shot = png;
      if (png && pins.indexOf(pin) >= 0) renderPins();
    }));
    renderPins();
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

  // Closed, so the app's own browser tests cannot see these controls: CRN adds
  // the widget after the AI's tests pass, then reruns them, and Playwright
  // pierces open roots (a page-wide button count found 💬 too). People and
  // screen readers still see and use it. Keep using `root`: the host's shadowRoot is null.
  var host = document.createElement("div");
  host.id = "fitt-feedback-host";
  var root = host.attachShadow({ mode: "closed" });
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
    ".pin img{width:64px;height:auto;max-height:48px;border-radius:4px;border:1px solid #eee;object-fit:cover;cursor:zoom-in}" +
    // A pin's picture full size, over everything; a click anywhere or Esc closes it.
    ".zoom{position:fixed;inset:0;z-index:2147483647;background:rgba(0,0,0,.8);display:flex;align-items:center;" +
    "justify-content:center;padding:24px;cursor:zoom-out}" +
    ".zoom[hidden]{display:none}" +
    ".zoom img{max-width:100%;max-height:100%;border-radius:8px;background:#fff;box-shadow:0 16px 48px rgba(0,0,0,.5)}" +
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
    // 🐞 error surface — a second FAB that only appears when errors are detected.
    ".efab{position:fixed;right:84px;bottom:20px;z-index:2147483647;width:56px;height:56px;border-radius:50%;" +
    "background:#ff4b4b;color:#fff;border:none;box-shadow:0 4px 0 #c93b3b,0 8px 24px rgba(0,0,0,.25);" +
    "font-size:24px;cursor:pointer;transition:transform .1s}" +
    ".efab:active{transform:translateY(4px);box-shadow:0 0 0 #c93b3b,0 4px 16px rgba(0,0,0,.25)}" +
    ".efab[hidden]{display:none}" +
    ".ebadge{position:absolute;top:-4px;right:-4px;min-width:20px;height:20px;padding:0 4px;border-radius:10px;" +
    "background:#fff;color:#ff4b4b;font-size:12px;font-weight:800;line-height:18px;border:2px solid #ff4b4b}" +
    ".elist{list-style:none;margin:0 0 10px;padding:0;display:flex;flex-direction:column;gap:6px}" +
    ".eitem{border:1px solid #ffdada;border-radius:8px;padding:8px;background:#fff6f6;text-align:left}" +
    ".emsg{font-size:12px;color:#c92a2a;font-weight:700;word-break:break-word}" +
    ".esrc{font-size:10px;color:#999;margin-top:3px;word-break:break-all}" +
    ".esend{background:#ff4b4b;box-shadow:0 4px 0 #c93b3b}.esend:active{box-shadow:0 0 0 #c93b3b}" +
    // 📋 ticket window: a button left of 💬 (🐞 moves one further left) and a modal.
    ".efab.shift{right:148px}" +
    ".tfab{position:fixed;right:84px;bottom:20px;z-index:2147483647;width:56px;height:56px;border-radius:50%;" +
    "background:#fff;border:2px solid #e5e5e5;box-shadow:0 4px 0 #d4d4d4,0 8px 24px rgba(0,0,0,.2);font-size:24px;cursor:pointer;transition:transform .1s}" +
    ".tfab:active{transform:translateY(4px);box-shadow:0 0 0 #d4d4d4,0 4px 16px rgba(0,0,0,.2)}" +
    ".tfab[hidden],.tbadge[hidden],.tscrim[hidden],.tfoot[hidden]{display:none}" +
    ".tbadge{position:absolute;top:-6px;right:-6px;min-width:22px;height:22px;padding:0 5px;border-radius:11px;background:#ff4b4b;" +
    "color:#fff;font-size:12px;font-weight:800;line-height:18px;border:2px solid #fff}" +
    ".tscrim{position:fixed;inset:0;z-index:2147483647;background:rgba(20,24,16,.42);display:flex;align-items:center;justify-content:center;padding:20px}" +
    ".tmodal{width:min(720px,100%);height:min(620px,100%);background:#fff;color:#3c3c3c;border-radius:18px;border:2px solid #e5e5e5;" +
    "box-shadow:0 24px 64px rgba(0,0,0,.35);display:flex;flex-direction:column;overflow:hidden}" +
    ".ttop{display:flex;justify-content:space-between;align-items:flex-start;gap:12px;padding:16px 18px 10px}" +
    ".ttop h3{margin:0 0 2px;font-size:17px}" +
    ".tsrc{margin:0;font-size:12px;color:#6f6f6f;line-height:1.5}.tsrc b{color:#3c3c3c}" +
    ".tclose{border:none;background:#f3f3f3;width:32px;height:32px;border-radius:50%;cursor:pointer;font-size:14px;color:#6f6f6f;flex:none}" +
    ".ttabs{display:flex;gap:6px;padding:0 18px 12px;border-bottom:2px solid #f0f0f0;overflow-x:auto;flex:none}" +
    ".ttab{padding:7px 12px;border:2px solid #e5e5e5;border-radius:10px;background:#fff;font-size:13px;font-weight:800;color:#6f6f6f;cursor:pointer;white-space:nowrap}" +
    ".ttab.on{border-color:" + GREEN + ";color:#2f7a00;background:rgba(88,204,2,.08)}.ttab:disabled{opacity:.5;cursor:default}" +
    ".tn{display:inline-block;min-width:20px;padding:0 6px;border-radius:999px;background:#ededed;font-size:11px;margin-left:4px;text-align:center}" +
    ".tn:empty{display:none}.ttab.on .tn{background:" + GREEN + ";color:#fff}" +
    ".tbody{flex:1;min-height:0;overflow:auto;padding:12px 18px}" +
    ".tfoot{flex:none;display:flex;align-items:center;justify-content:space-between;gap:12px;padding:12px 18px 16px;border-top:2px solid #f0f0f0}" +
    ".send.tgo{width:auto;padding:11px 18px}.tinfo{font-size:13px;color:#6f6f6f}" +
    ".tghost{padding:10px 14px;border:2px solid #e5e5e5;border-radius:12px;background:#fff;font-weight:800;color:#6f6f6f;cursor:pointer;font-size:13px}" +
    ".tlink{border:none;background:none;padding:0;color:#0b7fb5;font-weight:700;cursor:pointer;font-size:inherit;text-decoration:underline}" +
    ".tlisthead{display:flex;justify-content:space-between;align-items:center;gap:10px;flex-wrap:wrap;font-size:12px;color:#6f6f6f;margin-bottom:8px}" +
    // The panel's label style (small grey capitals) is for its field names, not these.
    ".tmodal label{text-transform:none;letter-spacing:normal;font-size:inherit;font-weight:400;color:inherit;margin:0}" +
    ".tmodal .tselall{display:flex;align-items:center;gap:8px;font-weight:700;color:#3c3c3c;cursor:pointer;padding-left:10px}" +
    ".tlist{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:8px}" +
    ".tk{border:2px solid #e5e5e5;border-radius:12px;padding:10px}.tk.on{border-color:" + GREEN + ";background:rgba(88,204,2,.05)}" +
    ".tpick{display:grid;grid-template-columns:26px minmax(0,1fr);gap:4px;cursor:pointer}" +
    ".tmodal input[type=checkbox]{width:18px;height:18px;accent-color:" + GREEN + ";margin:2px 0 0}" +
    ".tmain{min-width:0;display:flex;flex-direction:column;gap:6px}" +
    ".tmeta{display:flex;flex-wrap:wrap;gap:6px;align-items:center;font-size:11px;color:#6f6f6f}" +
    ".ttag{font-weight:800;padding:2px 8px;border-radius:999px;background:#f3f3f3;color:#555}" +
    ".ttag.high{background:#ffecec;color:#c92a2a}.ttag.med{background:#e8f6fe;color:#0b7fb5}" +
    ".tpath{font-family:ui-monospace,Menlo,monospace;font-size:11px;color:#2f7a00}" +
    ".ttxt{font-size:13px;line-height:1.55;overflow-wrap:anywhere}.tpins{font-size:11px;color:#6f6f6f}" +
    ".tempty{text-align:center;padding:40px 12px;color:#6f6f6f;font-size:13px;line-height:1.6}" +
    ".tempty b{display:block;color:#3c3c3c;font-size:15px;margin-bottom:4px}.terr{color:#c92a2a}" +
    ".tflash{margin:0 0 12px;padding:10px 12px;border-radius:10px;background:rgba(88,204,2,.12);color:#2f7a00;font-weight:700;font-size:13px}" +
    ".tlogin{text-align:center;padding:36px 12px;display:flex;flex-direction:column;align-items:center;gap:12px;font-size:13px;line-height:1.6;color:#6f6f6f}" +
    ".tlogin b{color:#3c3c3c;font-size:16px}.tlogin p{margin:0;max-width:44ch}" +
    ".tconfirm h4{margin:4px 0 6px;font-size:16px}.tconfirm p{margin:0 0 12px;font-size:13px;color:#6f6f6f}" +
    ".tpre{margin:0;padding:10px 12px;border-radius:10px;background:#f7f8f5;border:1px solid #e5e5e5;font-family:ui-monospace,Menlo,monospace;" +
    "font-size:12px;line-height:1.6;white-space:pre-wrap;overflow-wrap:anywhere}" +
    ".tsum{display:flex;flex-wrap:wrap;gap:6px;margin-bottom:12px}" +
    ".tbatch{margin-bottom:16px}.tbatch h5{font-size:12px;font-weight:800;color:#a3a3a3;margin:0 0 4px}" +
    ".trow{width:100%;display:grid;grid-template-columns:minmax(0,1fr) auto;gap:10px;align-items:center;text-align:left;border:none;" +
    "background:none;border-bottom:1px solid #f0f0f0;padding:10px 4px;cursor:pointer;color:inherit}" +
    ".ttitle{font-size:13px;line-height:1.45;min-width:0;overflow-wrap:anywhere}.ttitle small{display:block;color:#6f6f6f;font-size:11px;margin-top:2px}" +
    ".tpill{font-size:11px;font-weight:800;padding:3px 9px;border-radius:999px;white-space:nowrap}" +
    ".tpill.wait{background:#efefef;color:#5f5f5f}.tpill.doing{background:#fff4e5;color:#b25e00}.tpill.check{background:#e8f6fe;color:#0b7fb5}" +
    ".tpill.done{background:rgba(88,204,2,.15);color:#2f7a00}.tpill.wont{background:#f3f3f3;color:#8a8a8a}.tpill.bad{background:#ffecec;color:#c92a2a}" +
    ".tcode{font-family:ui-monospace,Menlo,monospace;font-size:28px;font-weight:800;letter-spacing:.12em;color:#3c3c3c;" +
    "padding:8px 16px;border:2px dashed #cfcfcf;border-radius:12px;background:#fafafa}" +
    "a.send{display:inline-block;text-decoration:none;text-align:center}" +
    ".tunder:empty{display:none}.tunder{padding:6px 0 0 30px}" +
    ".timgs{display:flex;flex-wrap:wrap;gap:6px;margin-top:6px}" +
    ".timgs img{width:72px;height:54px;object-fit:cover;border-radius:6px;border:1px solid #e5e5e5;cursor:zoom-in;background:#f3f3f3}" +
    ".tmore{text-align:center;margin:12px 0 0}.ttl li .tpill{margin-left:4px}" +
    ".tdetail{padding:8px 4px 14px;font-size:13px;line-height:1.6;border-bottom:1px solid #f0f0f0}.tdetail p{margin:0 0 8px}" +
    ".treply{background:#f5f8f2;border-radius:8px;padding:8px 10px}" +
    ".ttl{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:18px}" +
    ".tver{display:grid;grid-template-columns:52px minmax(0,1fr);gap:12px}" +
    ".tvtag{font-weight:800;font-size:15px;color:#fff;background:" + GREEN + ";border-radius:10px;height:32px;display:flex;align-items:center;" +
    "justify-content:center;box-shadow:0 3px 0 #61b800}.tvtag.old{background:#c4cbbc;box-shadow:0 3px 0 #a7ae9f}" +
    ".tver h4{margin:5px 0 8px;font-size:14px;display:flex;flex-wrap:wrap;gap:6px;align-items:center}.tver h4 small{font-weight:500;color:#6f6f6f;font-size:12px}" +
    ".tver ul{margin:0;padding:0;list-style:none;display:flex;flex-direction:column;gap:4px;font-size:13px;line-height:1.5}" +
    "@media (max-width:520px){.tscrim{padding:8px}.tmodal{height:100%}.ttop,.tbody,.ttabs{padding-left:12px;padding-right:12px}}" +
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
    "</div>" +
    "<button class='efab' hidden title='ตรวจพบ error — ส่งไปแก้'>🐞<span class='ebadge'>0</span></button>" +
    "<div class='panel epanel' hidden>" +
    "<h3>🐞 ตรวจพบ error ในระบบ</h3>" +
    "<ul class='elist'></ul>" +
    "<button class='send esend'>🛠 ส่งไปแก้</button>" +
    "<div class='status estatus'></div>" +
    "</div>" +
    "<button class='tfab' hidden title='ticket ของฉัน' aria-label='ticket ของฉัน'>📋<span class='tbadge' hidden>0</span></button>" +
    "<div class='tscrim' hidden><div class='tmodal' role='dialog' aria-modal='true' aria-label='ticket ของฉัน'>" +
    "<div class='ttop'><div><h3>📋 ticket ของฉัน</h3><p class='tsrc'></p></div><button class='tclose' aria-label='ปิด'>✕</button></div>" +
    "<div class='ttabs' role='tablist'><button class='ttab on' role='tab' data-tab='new'>รอส่ง<span class='tn tn-new'></span></button>" +
    "<button class='ttab' role='tab' data-tab='sent'>ส่งแล้ว<span class='tn tn-sent'></span></button>" +
    "<button class='ttab' role='tab' data-tab='history'>ประวัติการแก้ไข</button></div>" +
    "<div class='tbody'></div><div class='tfoot' hidden></div></div></div>" +
    "<div class='zoom' hidden title='คลิกเพื่อปิด'><img alt=''></div>";

  var fab = root.querySelector(".fab");
  var panel = root.querySelector(".panel");
  var pinbtn = root.querySelector(".pinbtn");
  var cropbtn = root.querySelector(".cropbtn");
  var pinsEl = root.querySelector(".pins");
  var noteEl = root.querySelector("textarea");
  var sendBtn = root.querySelector(".send");
  if (TICKETS) sendBtn.textContent = "บันทึก ticket";
  var statusEl = root.querySelector(".status");
  var zoom = root.querySelector(".zoom");
  var zoomImg = zoom.querySelector("img");

  function openZoom(src) {
    zoomImg.src = src;
    zoom.hidden = false;
  }
  function closeZoom() {
    zoom.hidden = true;
    zoomImg.removeAttribute("src");
  }
  zoom.addEventListener("click", closeZoom);
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && !zoom.hidden) closeZoom();
  });

  // --- 🐞 error capture (auto-detect) -----------------------------------------
  // A second FAB that only appears once an error is caught. Same confirm-to-send
  // model: it opens a panel listing the (deduped) errors; the operator reviews and
  // clicks "ส่งไปแก้" to POST them as category:"error" feedback (→ edit build).
  var efab = root.querySelector(".efab");
  var ebadge = root.querySelector(".ebadge");
  var epanel = root.querySelector(".epanel");
  var elistEl = root.querySelector(".elist");
  var esendBtn = root.querySelector(".esend");
  var estatusEl = root.querySelector(".estatus");
  var errors = [];

  function esc(s) {
    return String(s).replace(/[&<>]/g, function (c) { return c === "&" ? "&amp;" : c === "<" ? "&lt;" : "&gt;"; });
  }

  function captureError(source, message, stack, request) {
    message = String(message || "").trim();
    if (!message || message === "Script error.") return; // cross-origin noise
    var first = (stack || "").split("\n")[0] || "";
    var key = source + "|" + message + "|" + first;
    for (var i = 0; i < errors.length; i++) {
      if (errors[i].key === key) { errors[i].count++; renderErrors(); return; } // dedup
    }
    if (errors.length >= 50) return; // cap runaway loops
    errors.push({
      key: key, source: source, message: message.slice(0, 500),
      stack: String(stack || "").slice(0, 4000), request: request || null, count: 1,
    });
    renderErrors();
  }

  function renderErrors() {
    esendBtn.disabled = false;
    var n = errors.length;
    efab.hidden = n === 0;
    ebadge.textContent = String(n);
    if (n === 0) epanel.hidden = true;
    elistEl.innerHTML = errors.map(function (er) {
      var req = er.request ? " · " + er.request.status + " " + esc(er.request.url) : "";
      return "<li class='eitem'><div class='emsg'>" + esc(er.message) +
        (er.count > 1 ? " ×" + er.count : "") + "</div><div class='esrc'>" + esc(er.source) + req + "</div></li>";
    }).join("");
    esendBtn.textContent = "🛠 ส่งไปแก้ (" + n + ")";
  }

  efab.addEventListener("click", function () {
    panel.hidden = true; // don't overlap the feedback panel
    epanel.hidden = !epanel.hidden;
  });

  function esetStatus(msg, kind) {
    estatusEl.textContent = msg;
    estatusEl.className = "status estatus" + (kind ? " " + kind : "");
  }

  esendBtn.addEventListener("click", function () {
    if (!errors.length) return;
    esendBtn.disabled = true;
    esetStatus("กำลังส่ง…", "");
    var batch = errors.slice();
    Promise.all(batch.map(function (er) {
      return fetch(INGEST, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          project_id: PROJECT, category: "error", priority: "high",
          note: er.message, page_url: location.href, reporter: "",
          payload: {
            error: { message: er.message, stack: er.stack, source: er.source, request: er.request, count: er.count },
            viewport: { w: window.innerWidth, h: window.innerHeight }, user_agent: navigator.userAgent,
          },
        }),
      }).then(function (res) { if (!res.ok) throw new Error("HTTP " + res.status); });
    })).then(function () {
      esetStatus("ส่งไปแก้แล้ว ✓", "ok");
      errors = []; renderErrors();
      setTimeout(function () { epanel.hidden = true; esetStatus("", ""); }, 1500);
    }).catch(function (e) {
      esetStatus("ส่งไม่สำเร็จ — " + e.message, "err");
      esendBtn.disabled = false;
    });
  });

  // Global hooks + a bridge the app's React error boundary can call:
  //   window.__fittReportError(message, stack)  → shows up as a 🐞 error.
  window.addEventListener("error", function (e) {
    captureError("onerror", (e && e.message) || (e && e.error && e.error.message) || "Uncaught error",
      (e && e.error && e.error.stack) || (e && e.filename ? e.filename + ":" + e.lineno + ":" + e.colno : ""));
  });
  window.addEventListener("unhandledrejection", function (e) {
    var r = e && e.reason;
    captureError("unhandledrejection", (r && r.message) || String(r || "Unhandled rejection"), (r && r.stack) || "");
  });
  window.__fittReportError = function (message, stack, source) { captureError(source || "boundary", message, stack); };
  if (window.fetch) {
    var _fetch = window.fetch;
    window.fetch = function (input, init) {
      var url = typeof input === "string" ? input : (input && input.url) || "";
      var method = (init && init.method) || (input && input.method) || "GET";
      return _fetch.apply(this, arguments).then(function (res) {
        // 5xx = server error (a real bug). Skip our own ingest POSTs to avoid loops.
        if (res && res.status >= 500 && url.indexOf(INGEST) === -1 && (!TICKETS || url.indexOf(TICKETS) === -1)) {
          captureError("fetch", "HTTP " + res.status + " — " + method + " " + url, "",
            { url: url, method: method, status: res.status });
        }
        return res;
      });
    };
  }

  // --- 📋 ticket window -------------------------------------------------------
  // With data-tickets the signed-in app user sees the app's tickets in their
  // company's own fittcore and sends the ones they pick out of the company to DV.
  // Every call goes through the app's route, which holds the user's fittcore token
  // on the server (DV's contract, part A rev1.2): this page never sees it. Status
  // text and reasons are fittcore's own words, shown as they come.
  var tfab = root.querySelector(".tfab");
  var tbadge = root.querySelector(".tbadge");
  var tscrim = root.querySelector(".tscrim");
  var tsrc = root.querySelector(".tsrc");
  var tbody = root.querySelector(".tbody");
  var tfoot = root.querySelector(".tfoot");
  var tclose = root.querySelector(".tclose");
  var ttabs = root.querySelectorAll(".ttab");
  var TCAT = { bug: "🐞 bug", feature: "✨ feature", style: "🎨 style", error: "⚠️ error" };
  // Only the pill's colour comes from the status code; its words are status_label.
  var TPILL = { pending_send: "wait", withheld: "wont", sending: "doing", send_delayed: "doing", send_failed: "bad",
    received: "wait", in_progress: "doing", in_review: "check", fixed: "done", on_uat: "done", closed: "wont" };
  var tk = { view: "", tab: "new", items: [], source: null, cursor: null, loading: false, error: "", flash: "",
    sel: {}, open: {}, images: {}, confirm: null, busy: false, pending: null, note: "", name: "", polling: 0 };

  // ticketCall calls one op of the app's route; it answers { status, data } and
  // status 0 when the network failed.
  function ticketCall(method, op, params, body) {
    var url = TICKETS + (TICKETS.indexOf("?") >= 0 ? "&" : "?") + "op=" + op;
    for (var k in params) if (params[k] != null && params[k] !== "") url += "&" + k + "=" + encodeURIComponent(params[k]);
    var init = { method: method, credentials: "same-origin" };
    if (body) { init.headers = { "Content-Type": "application/json" }; init.body = JSON.stringify(body); }
    return fetch(url, init).then(function (res) {
      return res.text().then(function (text) {
        var data = null;
        try { data = text ? JSON.parse(text) : null; } catch (e) { data = null; }
        return { status: res.status, data: data || {} };
      });
    }, function () { return { status: 0, data: {} }; });
  }
  function ticketURL(op, params) {
    var url = TICKETS + (TICKETS.indexOf("?") >= 0 ? "&" : "?") + "op=" + op;
    for (var k in params) url += "&" + k + "=" + encodeURIComponent(params[k]);
    return url;
  }
  function escq(s) { return esc(s).replace(/"/g, "&quot;").replace(/'/g, "&#39;"); }
  function said(r, fallback) { return (r.data && r.data.message) || fallback; }

  // follow handles the answers every op may give: the site does not offer the
  // window, or the app user is not signed in.
  function follow(r) {
    if (r.status === 503 && r.data.error === "not_open") { show("closed"); return true; }
    if (r.status === 401 && r.data.error === "app_login") { show("applogin"); return true; }
    return false;
  }
  function show(view, note) {
    tk.view = view;
    tk.note = note || "";
    if (view !== "pending") tk.pending = null;
    renderTickets();
  }

  function refreshState() {
    ticketCall("GET", "state").then(function (r) {
      if (follow(r)) return;
      if (r.status !== 200) return show("login", said(r, "ติดต่อ fittcore ไม่สำเร็จ ลองใหม่อีกครั้ง"));
      if (r.data.ready) { tk.name = r.data.name || ""; tk.view = "ready"; loadTickets(true); return; }
      if (r.data.pending) { tk.pending = r.data.pending; show("pending"); poll(); return; }
      show("login");
    });
  }

  function startLogin() {
    tk.busy = true;
    renderTickets();
    ticketCall("POST", "login", null, { project: PROJECT }).then(function (r) {
      tk.busy = false;
      if (follow(r)) return;
      if (r.status === 200 && r.data.ready) { tk.name = r.data.name || ""; tk.view = "ready"; return loadTickets(true); }
      if (r.status === 200 && r.data.pending) { tk.pending = r.data.pending; show("pending"); return poll(); }
      show("login", said(r, "ขอรหัสจาก fittcore ไม่สำเร็จ ลองใหม่อีกครั้ง"));
    });
  }

  // poll asks the route whether the user approved, at the pace fittcore set; the
  // route keeps the poll secret and keeps to that pace too.
  function poll() {
    if (tk.polling) return;
    var wait = (tk.pending && tk.pending.interval) || 3000;
    tk.polling = setTimeout(function () {
      tk.polling = 0;
      if (tk.view !== "pending") return;
      ticketCall("POST", "poll").then(function (r) {
        if (follow(r)) return;
        if (r.status === 200 && r.data.ready) { tk.name = r.data.name || ""; tk.view = "ready"; return loadTickets(true); }
        if (r.status === 200 && r.data.pending) { tk.pending = r.data.pending; renderTickets(); return poll(); }
        if (r.status === 200 && r.data.denied) return show("login", "ไม่ได้รับอนุมัติใน fittcore");
        if (r.status === 200 && r.data.expired) return show("login", "รหัสหมดอายุแล้ว ขอรหัสใหม่อีกครั้ง");
        poll();
      });
    }, wait);
  }

  function loadTickets(reset) {
    if (reset) { tk.cursor = null; }
    tk.loading = true;
    tk.error = "";
    renderTickets();
    ticketCall("GET", "list", { status: "all", cursor: reset ? "" : tk.cursor }).then(function (r) {
      tk.loading = false;
      if (follow(r)) return;
      if (r.status === 401 || r.status === 403) return show("login", said(r, "สิทธิ์หมดอายุแล้ว ขอรหัสใหม่อีกครั้ง"));
      if (r.status !== 200 || !Array.isArray(r.data.tickets)) { tk.error = said(r, "โหลดรายการไม่สำเร็จ"); return renderTickets(); }
      var fresh = r.data.tickets.filter(function (f) { return f && typeof f.id === "string"; });
      tk.items = reset ? fresh : tk.items.concat(fresh);
      tk.source = r.data.source || null;
      tk.cursor = r.data.next_cursor || null;
      var keep = {};
      tk.items.forEach(function (f) { if (f.status === "pending_send" && tk.sel[f.id]) keep[f.id] = true; });
      tk.sel = keep;
      renderTickets();
    });
  }

  function mode() { return (tk.source && tk.source.forward_mode) || "triage"; }
  function waiting() { return tk.items.filter(function (f) { return f.status === "pending_send"; }); }
  function others() { return tk.items.filter(function (f) { return f.status !== "pending_send"; }); }
  function picked() { return waiting().filter(function (f) { return tk.sel[f.id]; }).map(function (f) { return f.id; }); }
  function when(iso) {
    var d = new Date(iso);
    if (!iso || isNaN(d.getTime())) return "";
    try { return d.toLocaleString("th-TH", { dateStyle: "medium", timeStyle: "short" }); } catch (e) { return d.toLocaleString(); }
  }
  function pill(f) {
    return f.status_label ? "<span class='tpill " + (TPILL[f.status] || "wait") + "'>" + esc(f.status_label) + "</span>" : "";
  }
  function meta(f) {
    return "<span class='ttag'>" + (TCAT[f.category] || esc(f.category || "")) + "</span>" +
      (f.priority ? "<span class='ttag " + escq(f.priority) + "'>" + esc(f.priority) + "</span>" : "") +
      (f.page ? "<span class='tpath'>" + esc(f.page) + "</span>" : "") +
      (f.created_at ? "<span>" + esc(when(f.created_at)) + "</span>" : "");
  }
  function text(f) { return String(f.text || "").trim() || "(ไม่มีข้อความ)"; }
  // A ticket's pictures, fetched when the user asks, through the route.
  function imagesHTML(f) {
    var got = tk.images[f.id];
    if (!f.image_count) return "";
    if (!got) return "<button class='tlink' data-images='" + escq(f.id) + "'>ดูรูป (" + f.image_count + ")</button>";
    if (got === "loading") return "<span class='tpins'>กำลังโหลดรูป…</span>";
    if (typeof got === "string") return "<span class='tpins terr'>" + esc(got) + "</span>";
    return "<span class='timgs'>" + got.map(function (im) {
      var src = ticketURL("image", { id: f.id, image: im.image_id });
      return "<img src='" + escq(src) + "' alt='' data-zoom='" + escq(src) + "'>";
    }).join("") + "</span>";
  }
  function loadImages(id) {
    tk.images[id] = "loading";
    renderTickets();
    ticketCall("GET", "images", { id: id }).then(function (r) {
      if (follow(r)) return;
      tk.images[id] = r.status === 200 && Array.isArray(r.data.images) ? r.data.images : said(r, "โหลดรูปไม่สำเร็จ");
      renderTickets();
    });
  }

  function renderTickets() {
    if (!TICKETS) return;
    var ready = tk.view === "ready";
    var n = ready ? waiting().length : 0;
    tbadge.textContent = String(n);
    tbadge.hidden = n === 0;
    if (tscrim.hidden) return;
    ttabs.forEach(function (b) {
      var current = b.getAttribute("data-tab") === tk.tab;
      b.classList.toggle("on", current);
      b.setAttribute("aria-selected", current ? "true" : "false");
      b.disabled = !ready;
    });
    root.querySelector(".tn-new").textContent = ready ? String(n) : "";
    root.querySelector(".tn-sent").textContent = ready ? String(others().length) : "";
    tsrc.innerHTML = ready
      ? "อนุมัติโดย <b>" + esc(tk.name || "บัญชี fittcore ของคุณ") + "</b> · <button class='tlink' data-act='reload'>โหลดใหม่</button> · <button class='tlink' data-act='logout'>เลิกใช้สิทธิ์นี้</button>"
      : "รายการจาก fittcore ของบริษัทคุณ";
    tfoot.hidden = true;
    if (tk.view === "closed") {
      tbody.innerHTML = "<div class='tempty'><b>ยังไม่เปิดใช้</b>ตอนนี้ยังดูและส่ง ticket จากหน้านี้ไม่ได้ ทีม DV จะแจ้งสถานะ feedback ให้ทราบ</div>";
      return;
    }
    if (tk.view === "applogin") {
      tbody.innerHTML = "<div class='tempty'><b>ล็อกอินแอปก่อน</b>ล็อกอินแอปนี้ด้วยบัญชี fittcore ของบริษัท แล้วเปิด 📋 อีกครั้ง</div>";
      return;
    }
    if (tk.view === "login") {
      tbody.innerHTML = "<div class='tlogin'><b>ขอสิทธิ์จาก fittcore ก่อน</b>" +
        "<p>ระบบจะขอรหัสให้บัญชีที่คุณล็อกอินแอปอยู่ คุณต้องเป็น admin ของบริษัท และอนุมัติด้วยบัญชีเดียวกันใน fittcore</p>" +
        (tk.note ? "<p class='terr'>" + esc(tk.note) + "</p>" : "") +
        "<button class='send tgo' data-act='login'" + (tk.busy ? " disabled" : "") + ">" + (tk.busy ? "กำลังขอรหัส…" : "ขอรหัสอนุมัติ") + "</button></div>";
      return;
    }
    if (tk.view === "pending" && tk.pending) {
      tbody.innerHTML = "<div class='tlogin'><b>อนุมัติใน fittcore</b>" +
        "<p>เปิดหน้าอนุมัติ ล็อกอินด้วยบัญชีเดียวกับที่ใช้แอปนี้ ตรวจว่ารหัสตรงกับที่เห็นตรงนี้ แล้วกด \"อนุมัติ\"</p>" +
        "<span class='tcode'>" + esc(tk.pending.code) + "</span>" +
        "<a class='send tgo' href='" + escq(tk.pending.url) + "' target='_blank' rel='noopener'>เปิดหน้าอนุมัติ</a>" +
        "<p class='tpins'>รอการอนุมัติ… รหัสหมดอายุ " + esc(when(new Date(tk.pending.expires).toISOString())) + "</p></div>";
      return;
    }
    if (!ready) { tbody.innerHTML = "<div class='tempty'>กำลังโหลด…</div>"; return; }
    if (tk.error) {
      tbody.innerHTML = "<div class='tempty'><b class='terr'>" + esc(tk.error) + "</b><button class='tlink' data-act='reload'>ลองโหลดใหม่</button></div>";
      return;
    }
    if (tk.tab === "new") renderWaiting();
    else if (tk.tab === "sent") renderOthers();
    else renderBuilds();
  }

  function more() {
    return tk.cursor ? "<p class='tmore'><button class='tlink' data-act='more'" + (tk.loading ? " disabled" : "") + ">" +
      (tk.loading ? "กำลังโหลด…" : "โหลดเพิ่ม") + "</button></p>" : "";
  }

  function renderWaiting() {
    if (tk.confirm) return renderConfirm();
    var list = waiting();
    var h = tk.flash ? "<p class='tflash' role='status'>" + esc(tk.flash) + "</p>" : "";
    if (mode() === "auto") h += "<p class='tpins'>แอปนี้ส่ง feedback ให้ DV อัตโนมัติ ไม่ต้องกดส่ง</p>";
    if (mode() === "off") h += "<p class='tpins'>บริษัทปิดการส่ง feedback ต่อไว้</p>";
    if (!list.length) {
      tbody.innerHTML = h + (tk.loading && !tk.items.length ? "<div class='tempty'>กำลังโหลด…</div>"
        : "<div class='tempty'><b>ไม่มี ticket ที่รอส่ง</b>กด 💬 เพื่อเขียน ticket ใหม่</div>") + more();
      return;
    }
    var canSend = mode() === "triage";
    var all = list.every(function (f) { return tk.sel[f.id]; });
    if (canSend) {
      h += "<div class='tlisthead'><label class='tselall'><input type='checkbox' data-all" + (all ? " checked" : "") +
        "> เลือกทั้งหมด</label><span>เขียนไว้แล้ว ยังไม่ได้ส่งไปฐานกลาง</span></div>";
    }
    h += "<ul class='tlist'>";
    list.forEach(function (f) {
      var on = !!tk.sel[f.id];
      var body = "<span class='tmain'><span class='tmeta'>" + meta(f) + pill(f) + "</span><span class='ttxt'>" + esc(text(f)) + "</span></span>";
      h += "<li class='tk" + (on ? " on" : "") + "'>" +
        (canSend ? "<label class='tpick'><input type='checkbox' data-pick='" + escq(f.id) + "'" + (on ? " checked" : "") + ">" + body + "</label>" : body) +
        "<div class='tunder'>" + imagesHTML(f) + "</div></li>";
    });
    tbody.innerHTML = h + "</ul>" + more();
    if (!canSend) return;
    var n = picked().length;
    tfoot.hidden = false;
    tfoot.innerHTML = "<span class='tinfo'>เลือก " + n + " จาก " + list.length + " รายการ</span>" +
      "<button class='send tgo' data-act='ask'" + (n ? "" : " disabled") + ">ส่งไปฐานกลาง" + (n ? " (" + n + ")" : "") + "</button>";
  }

  // newKey is the Idempotency-Key of one press of "ยืนยันส่ง": a retry after a
  // network failure sends it again, a new press gets a new one.
  function newKey() {
    var a = new Uint8Array(18);
    try { crypto.getRandomValues(a); } catch (e) { for (var i = 0; i < a.length; i++) a[i] = Math.floor(Math.random() * 256); }
    return btoa(String.fromCharCode.apply(null, a)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function renderConfirm() {
    var c = tk.confirm;
    var h = "<div class='tconfirm'><h4>ส่ง " + c.ids.length + " ticket ไปฐานกลาง?</h4>" +
      "<p>ticket เหล่านี้จะออกจากบริษัทคุณไปถึงทีม DV ส่งแล้วดึงกลับไม่ได้</p><ul class='tlist'>";
    c.ids.forEach(function (id) {
      var f = tk.items.filter(function (x) { return x.id === id; })[0];
      if (f) h += "<li class='tk'><span class='tmain'><span class='tmeta'>" + meta(f) + "</span><span class='ttxt'>" + esc(text(f)) + "</span></span></li>";
    });
    tbody.innerHTML = h + "</ul>" + (c.error ? "<p class='terr'>" + esc(c.error) + "</p>" : "") + "</div>";
    tfoot.hidden = false;
    tfoot.innerHTML = "<button class='tghost' data-act='back'" + (tk.busy ? " disabled" : "") + ">กลับไปเลือก</button>" +
      "<button class='send tgo' data-act='send'" + (tk.busy ? " disabled" : "") + ">" +
      (tk.busy ? "กำลังส่ง…" : c.error ? "ลองส่งอีกครั้ง" : "ยืนยันส่ง " + c.ids.length + " รายการ") + "</button>";
  }

  function sendPicked() {
    var c = tk.confirm;
    tk.busy = true;
    renderTickets();
    ticketCall("POST", "submit", null, { ticket_ids: c.ids, key: c.key }).then(function (r) {
      tk.busy = false;
      if (follow(r)) return;
      if (r.status === 401 || r.status === 403) { tk.confirm = null; return show("login", said(r, "สิทธิ์หมดอายุแล้ว ขอรหัสใหม่แล้วส่งอีกครั้ง")); }
      if (r.status !== 200 || !Array.isArray(r.data.results)) {
        // A network failure or a busy fittcore: the same press, so the same key.
        c.error = said(r, r.status === 0 ? "ส่งไม่สำเร็จ เครือข่ายขัดข้อง" : "ส่งไม่สำเร็จ");
        return renderTickets();
      }
      var count = {};
      r.data.results.forEach(function (x) { count[x.result] = (count[x.result] || 0) + 1; });
      var parts = [];
      if (count.queued) parts.push("ส่งแล้ว " + count.queued + " รายการ");
      if (count.already_sent) parts.push("ส่งไปก่อนหน้าแล้ว " + count.already_sent);
      if (count.auto) parts.push("ส่งอัตโนมัติแล้ว " + count.auto);
      if (count.not_allowed) parts.push("ส่งไม่ได้ " + count.not_allowed);
      if (count.not_found) parts.push("ไม่พบ " + count.not_found);
      tk.confirm = null;
      tk.sel = {};
      tk.tab = "sent";
      tk.flash = parts.join(" · ") + " ✓";
      loadTickets(true);
    });
  }

  function renderOthers() {
    var list = others();
    var h = tk.flash ? "<p class='tflash' role='status'>" + esc(tk.flash) + "</p>" : "";
    if (!list.length) {
      tbody.innerHTML = h + "<div class='tempty'><b>ยังไม่มี ticket ที่ส่งไป</b>ticket ที่ส่งไปฐานกลางแล้วจะขึ้นที่นี่ พร้อมสถานะ</div>" + more();
      return;
    }
    list.forEach(function (f) {
      var open = !!tk.open[f.id];
      h += "<button class='trow' data-open='" + escq(f.id) + "' aria-expanded='" + open + "'><span class='ttitle'>" + esc(text(f)) +
        "<small>" + (TCAT[f.category] || esc(f.category || "")) + (f.page ? " · " + esc(f.page) : "") +
        (f.sent_at ? " · ส่งเมื่อ " + esc(when(f.sent_at)) : "") + "</small></span>" + pill(f) + "</button>";
      if (open) {
        h += "<div class='tdetail'><p>" + esc(text(f)) + "</p>" +
          (f.status === "closed" && f.customer_reason ? "<p class='treply'><b>จาก DV</b> " + esc(f.customer_reason) + "</p>" : "") +
          "<p class='tpins'>เขียนเมื่อ " + esc(when(f.created_at)) + "</p>" + imagesHTML(f) + "</div>";
      }
    });
    tbody.innerHTML = h + more();
  }

  function renderBuilds() {
    var builds = {};
    var order = [];
    tk.items.forEach(function (f) {
      if (f.build_no == null) return;
      var n = String(f.build_no);
      if (!builds[n]) { builds[n] = []; order.push(n); }
      builds[n].push(f);
    });
    order.sort(function (a, b) { return (Number(b) || 0) - (Number(a) || 0); });
    if (!order.length) {
      tbody.innerHTML = "<div class='tempty'><b>ยังไม่มีประวัติการแก้ไข</b>ticket ที่แก้แล้วจะขึ้นที่นี่ แยกตามรอบ build</div>" + more();
      return;
    }
    var h = "<ol class='ttl'>";
    order.forEach(function (n, i) {
      h += "<li class='tver'><span class='tvtag" + (i ? " old" : "") + "'>#" + esc(n) + "</span><div><h4>build #" + esc(n) + "</h4><ul>";
      builds[n].forEach(function (f) { h += "<li>" + esc(text(f)) + " " + pill(f) + "</li>"; });
      h += "</ul></div></li>";
    });
    tbody.innerHTML = h + "</ol>" + more();
  }

  // showTicketButton puts 📋 left of 💬 (🐞 moves one further left), or takes it away.
  function showTicketButton(on) {
    tfab.hidden = !on;
    if (on) efab.classList.add("shift");
    else efab.classList.remove("shift");
  }

  function closeTickets() {
    tscrim.hidden = true;
    tk.confirm = null;
    // A site that turned out not to offer the window keeps no button that cannot work.
    if (tk.view === "closed") { showTicketButton(false); fab.focus(); return; }
    tfab.focus();
  }

  if (TICKETS) {
    // 📋 shows only when the app's route says the window is open: the app's server
    // turned it on (FITT_TICKET_WINDOW=1) and its company fittcore has the widget API.
    ticketCall("GET", "open").then(function (r) {
      if (r.status === 200 && r.data.open === true) showTicketButton(true);
    });
    tfab.addEventListener("click", function () {
      panel.hidden = true;
      epanel.hidden = true;
      tscrim.hidden = false;
      tk.flash = "";
      if (tk.view === "ready") loadTickets(true);
      else if (tk.view === "pending") renderTickets();
      else { tk.view = ""; renderTickets(); refreshState(); }
      tclose.focus();
    });
    tclose.addEventListener("click", closeTickets);
    tscrim.addEventListener("click", function (e) { if (e.target === tscrim) closeTickets(); });
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && !tscrim.hidden && zoom.hidden) closeTickets();
    });
    ttabs.forEach(function (b) {
      b.addEventListener("click", function () {
        tk.tab = b.getAttribute("data-tab");
        tk.confirm = null;
        if (tk.tab !== "sent") tk.flash = "";
        renderTickets();
      });
    });
    tsrc.addEventListener("click", function (e) {
      var b = e.target.closest("button");
      if (!b) return;
      if (b.getAttribute("data-act") === "reload") loadTickets(true);
      if (b.getAttribute("data-act") === "logout") {
        ticketCall("POST", "logout").then(function (r) {
          if (follow(r)) return;
          tk.items = []; tk.sel = {}; tk.images = {};
          show("login");
        });
      }
    });
    tbody.addEventListener("change", function (e) {
      var el = e.target;
      if (el.hasAttribute("data-all")) waiting().forEach(function (f) { tk.sel[f.id] = el.checked; });
      else if (el.getAttribute("data-pick")) tk.sel[el.getAttribute("data-pick")] = el.checked;
      else return;
      renderTickets();
      var again = el.hasAttribute("data-all") ? tbody.querySelector("[data-all]")
        : tbody.querySelector("[data-pick='" + el.getAttribute("data-pick") + "']");
      if (again) again.focus();
    });
    tbody.addEventListener("click", function (e) {
      var img = e.target.closest("img[data-zoom]");
      if (img) return openZoom(img.getAttribute("data-zoom"));
      var b = e.target.closest("button");
      if (!b) return;
      var act = b.getAttribute("data-act");
      if (act === "login") startLogin();
      else if (act === "reload") loadTickets(true);
      else if (act === "more") loadTickets(false);
      else if (b.getAttribute("data-images")) loadImages(b.getAttribute("data-images"));
      else if (b.getAttribute("data-open")) {
        var id = b.getAttribute("data-open");
        tk.open[id] = !tk.open[id];
        renderTickets();
        var again = tbody.querySelector("[data-open='" + id + "']");
        if (again) again.focus();
      }
    });
    tfoot.addEventListener("click", function (e) {
      var b = e.target.closest("button");
      if (!b || b.disabled) return;
      var act = b.getAttribute("data-act");
      if (act === "ask") { tk.confirm = { ids: picked(), key: newKey(), error: "" }; renderTickets(); }
      else if (act === "back") { tk.confirm = null; renderTickets(); }
      else if (act === "send" && tk.confirm) sendPicked();
    });
  }

  fab.addEventListener("click", function () {
    epanel.hidden = true; // don't overlap the error panel
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
      var shot = li.querySelector("img");
      if (shot) shot.addEventListener("click", function () { openZoom(p.region_shot); });
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
    addPin({
      selector: cssPath(el),
      label: shortLabel(el),
      note: "",
      box: pageBox(el),
      region_shot: "",
    }, captureElement(el));
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
    addPin({
      selector: "region",
      label: "พื้นที่ " + Math.round(w) + "×" + Math.round(h),
      note: "",
      box: {
        x: Math.round(x + window.scrollX),
        y: Math.round(y + window.scrollY),
        w: Math.round(w),
        h: Math.round(h),
      },
      region_shot: "",
    }, captureRegion(x, y, w, h));
  }

  // Capture a viewport rectangle by cloning <body> into an SVG foreignObject,
  // offset so the selected rect aligns to the image origin, then painting it to
  // PNG (svgToPNG). Cross-origin images won't render (CORS) but layout/text/colors
  // do — enough to point at a spot.
  function captureRegion(vx, vy, w, h) {
    try {
      w = Math.max(1, Math.round(w));
      h = Math.max(1, Math.round(h));
      var body = document.body;
      var clone = body.cloneNode(true);
      // Only rasterize images inside the crop rectangle (viewport coords), so a
      // small crop over an image-heavy page stays small instead of embedding
      // every full-res image on the page.
      // The body keeps its own margin: the copy sits at the page origin, so the
      // margin is what puts the content where the user saw it.
      inlineStyles(body, clone, { left: vx, top: vy, right: vx + w, bottom: vy + h });
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
      return svgToPNG("data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg), w, h);
    } catch (e) {
      return Promise.resolve("");
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
    Promise.all(shots)
      .then(function () {
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
        return fetch(INGEST, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
      })
      .then(function (res) {
        if (!res.ok) throw new Error("HTTP " + res.status);
        pins = []; shots = []; renderPins(); noteEl.value = "";
        if (TICKETS) {
          // The ticket waits in the company's fittcore until it is sent from 📋,
          // unless the app forwards every ticket by itself.
          setStatus(mode() === "auto" ? "บันทึกแล้ว ✓ ระบบส่งต่อให้ DV เอง" : "บันทึกแล้ว ✓ กด 📋 เพื่อเลือกส่งไปฐานกลาง", "ok");
          if (tk.view === "ready") loadTickets(true);
          setTimeout(function () { panel.hidden = true; setStatus("", ""); }, 2600);
          return;
        }
        setStatus("ส่งแล้ว ขอบคุณ! ✓", "ok");
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
