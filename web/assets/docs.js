/*
 * Documentation viewer.
 *
 * The content lives in docs.html, one <template data-topic> per page. This file
 * builds the topic menu, shows one page at a time, searches page text, and
 * dresses the code blocks. It holds no documentation text.
 */
(function () {
  "use strict";

  var navEl = document.getElementById("docs-nav");
  var contentEl = document.getElementById("doc-content");
  var searchEl = document.getElementById("docs-search");
  var sidebarEl = document.getElementById("docs-sidebar");
  var mobileToggleEl = document.getElementById("docs-mobile-toggle");
  var mobileCurrentEl = document.getElementById("docs-mobile-current");
  if (!navEl || !contentEl) return;

  function escapeHtml(text) {
    return String(text)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;");
  }

  // Pages, in the order they appear in docs.html.
  var topics = [];
  var topicById = {};
  Array.prototype.slice.call(document.querySelectorAll("template[data-topic]")).forEach(function (tpl) {
    var topic = {
      id: tpl.getAttribute("data-topic"),
      group: tpl.getAttribute("data-group") || "",
      title: tpl.getAttribute("data-title") || "",
      tpl: tpl,
      text: (tpl.content.textContent || "").toLowerCase()
    };
    topics.push(topic);
    topicById[topic.id] = topic;
  });
  if (topics.length === 0) return;
  var firstId = topics[0].id;

  // Colors a report the way the terminal does: pass green, warning yellow, error red.
  function paintReport(text) {
    return escapeHtml(text).split("\n").map(function (line) {
      if (/^(stageproof|verdict)\s/.test(line)) {
        line = line
          .replace(/(PROVEN|PASSED)( \(exit \d\))?/, '<span class="ansi-g">$1$2</span>')
          .replace(/(BLOCKED)( \(exit \d\))?/, '<span class="ansi-r">$1$2</span>')
          .replace(/(NOT CHECKED)( \(exit \d\))?/, '<span class="ansi-y">$1$2</span>')
          .replace(/(exit \d|· .*)$/, '<span class="ansi-d">$1</span>');
      }
      return line
        .replace(/^(stageproof|compare|values|k8s|verdict)(\s)/, '<span class="ansi-d">$1</span>$2')
        .replace(/(✓ PASS)/, '<span class="ansi-g">$1</span>')
        .replace(/(✕ FAIL)/, '<span class="ansi-r">$1</span>')
        .replace(/^(\s*)(! WARN\s+\S+)/, '$1<span class="ansi-y">$2</span>')
        .replace(/^(\s*)(WARN\s+\S+)/, '$1<span class="ansi-y">$2</span>')
        .replace(/^(\s*)(ERROR\s+\S+)/, '$1<span class="ansi-r">$2</span>')
        .replace(/^(\s{9})(runtime_rehearsal_not_enabled|fix:)/, '$1<span class="ansi-y">$2</span>');
    }).join("\n");
  }

  // Wraps each <pre data-kind> in a titled frame. Commands and files get a Copy button.
  function dressCodeBlocks(root) {
    Array.prototype.slice.call(root.querySelectorAll("pre[data-kind]")).forEach(function (pre) {
      var kind = pre.getAttribute("data-kind");
      var raw = pre.textContent.replace(/\n$/, "");
      var label = pre.getAttribute("data-label") || (kind === "out" ? "Output" : kind === "file" ? "File" : "Command");

      var wrap = document.createElement("div");
      wrap.className = "doc-code-wrap doc-code-" + kind;
      var head = document.createElement("div");
      head.className = "doc-code-head";
      var name = document.createElement("span");
      name.textContent = label;
      head.appendChild(name);

      if (kind !== "out") {
        var copy = document.createElement("button");
        copy.type = "button";
        copy.className = "copy-btn";
        copy.textContent = "Copy";
        copy.addEventListener("click", function () {
          var done = function () {
            copy.textContent = "Copied";
            window.setTimeout(function () { copy.textContent = "Copy"; }, 1300);
          };
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(raw).then(done).catch(done);
          } else {
            done();
          }
        });
        head.appendChild(copy);
      }

      var code = document.createElement("code");
      if (kind === "out") code.innerHTML = paintReport(raw);
      else code.textContent = raw;
      pre.textContent = "";
      pre.appendChild(code);

      pre.parentNode.insertBefore(wrap, pre);
      wrap.appendChild(head);
      wrap.appendChild(pre);
    });
  }

  function renderNav(activeId, query) {
    var q = (query || "").trim().toLowerCase();
    var groups = [];
    var byGroup = {};
    topics.forEach(function (topic) {
      if (q && topic.title.toLowerCase().indexOf(q) === -1 && topic.text.indexOf(q) === -1) return;
      if (!byGroup[topic.group]) {
        byGroup[topic.group] = [];
        groups.push(topic.group);
      }
      byGroup[topic.group].push(topic);
    });

    if (groups.length === 0) {
      navEl.innerHTML = '<div class="docs-empty-filter">No page mentions "' + escapeHtml(query) + '".</div>';
      return;
    }

    navEl.innerHTML = groups.map(function (group) {
      return '<div class="docs-group"><div class="docs-group-title">' + escapeHtml(group) + '</div><ul class="docs-nav-list">' +
        byGroup[group].map(function (topic) {
          var cls = topic.id === activeId ? "docs-nav-btn is-active" : "docs-nav-btn";
          var current = topic.id === activeId ? ' aria-current="page"' : "";
          return '<li><a class="' + cls + '" href="#' + topic.id + '"' + current + '>' + escapeHtml(topic.title) + '</a></li>';
        }).join("") +
        '</ul></div>';
    }).join("");
  }

  function pagerHtml(index) {
    var prev = index > 0 ? topics[index - 1] : null;
    var next = index < topics.length - 1 ? topics[index + 1] : null;
    var html = '<nav class="docs-pager" aria-label="Previous and next page">';
    html += prev
      ? '<a class="docs-pager-btn" href="#' + prev.id + '"><span class="docs-pager-dir">Previous</span><span class="docs-pager-title">' + escapeHtml(prev.title) + '</span></a>'
      : '<span></span>';
    if (next) {
      html += '<a class="docs-pager-btn is-next" href="#' + next.id + '"><span class="docs-pager-dir">Next</span><span class="docs-pager-title">' + escapeHtml(next.title) + '</span></a>';
    }
    return html + '</nav>';
  }

  function show(id) {
    var topic = topicById[id] || topicById[firstId];
    var index = topics.indexOf(topic);

    contentEl.innerHTML =
      '<div class="docs-breadcrumb"><span>Docs</span><span class="sep">/</span><span>' + escapeHtml(topic.group) + '</span></div>' +
      '<h1>' + escapeHtml(topic.title) + '</h1>';
    contentEl.appendChild(topic.tpl.content.cloneNode(true));
    dressCodeBlocks(contentEl);
    contentEl.insertAdjacentHTML("beforeend", pagerHtml(index));

    renderNav(topic.id, searchEl ? searchEl.value : "");
    if (mobileCurrentEl) mobileCurrentEl.textContent = topic.title;
    if (sidebarEl && mobileToggleEl) {
      sidebarEl.classList.remove("is-open");
      mobileToggleEl.setAttribute("aria-expanded", "false");
    }
    document.title = topic.title + " - Stageproof Docs";
    window.scrollTo(0, 0);
  }

  function currentId() {
    var hash = (window.location.hash || "").slice(1);
    return topicById[hash] ? hash : firstId;
  }

  if (searchEl) {
    searchEl.addEventListener("input", function () {
      renderNav(currentId(), searchEl.value);
    });
  }
  if (mobileToggleEl && sidebarEl) {
    mobileToggleEl.addEventListener("click", function () {
      var open = sidebarEl.classList.toggle("is-open");
      mobileToggleEl.setAttribute("aria-expanded", open ? "true" : "false");
    });
  }
  window.addEventListener("hashchange", function () { show(currentId()); });

  show(currentId());
})();
