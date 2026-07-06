(() => {
  let source;
  let restoreFocusSelector;
  const ansiColors = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];

  function connectRunStream() {
    const root = document.querySelector("[data-run-stream]");
    if (!root) {
      if (source) {
        source.close();
        source = undefined;
      }
      return;
    }

    const url = root.getAttribute("data-run-stream");
    if (!url || (source && source.url.endsWith(url))) return;
    if (source) source.close();

    source = new EventSource(url);
    source.addEventListener("status", (event) => {
      const data = JSON.parse(event.data);
      const status = document.getElementById("run-status");
      if (status) status.textContent = data.status || data.type || "running";
      const progress = document.querySelector(".progress span");
      if (progress && data.task_total) {
        const completed = Math.max(0, data.task_index || 0);
        progress.style.width = `${Math.min(100, Math.round((completed / data.task_total) * 100))}%`;
      }
    });
    source.addEventListener("log", (event) => {
      const data = JSON.parse(event.data);
      const log = document.getElementById("run-log");
      if (!log || !data.line) return;
      const line = document.createElement("div");
      line.className = ["log-line", logLineClass(data)].filter(Boolean).join(" ");
      line.innerHTML = renderTerminalLine(data.line);
      log.appendChild(line);
      log.scrollTop = log.scrollHeight;
    });
    source.addEventListener("recovery", (event) => {
      const data = JSON.parse(event.data);
      const panel = document.getElementById("recovery-panel");
      if (!panel) return;
      if (data.html) {
        panel.innerHTML = data.html;
        return;
      }
      panel.innerHTML = `<div class="recovery"><div class="alert"><strong>${escapeHTML(data.kind || "failed")}</strong>: <span>${escapeHTML(data.message || "Run failed.")}</span></div></div>`;
    });
    source.addEventListener("done", (event) => {
      const data = JSON.parse(event.data);
      const status = document.getElementById("run-status");
      if (status && data.status) status.textContent = data.status;
      if (source) source.close();
    });
  }

  function escapeHTML(value) {
    return String(value).replace(/[&<>"']/g, (ch) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    })[ch]);
  }

  function logLineClass(data) {
    const line = String(data.line || "");
    if (data.stream === "stderr" || /\b(error|failed|failure|fatal|denied|context canceled)\b/i.test(line)) return "err";
    if (/^\s*(\[ok\]|ok:|success|run complete)/i.test(line)) return "ok";
    if (/^\s*(warning|warn|\[warn\])/i.test(line)) return "warn";
    if (/^\s*(running|preparing|configuration repository ready|one-time action|one-time stage|cancelling|run cancelled)/i.test(line)) return "info";
    if (/^\s*(hit:|get:|reading |building |solving |[0-9]+ upgraded|ca-certificates|curl |gnupg |ufw )/i.test(line)) return "muted";
    return "";
  }

  function renderTerminalLine(value) {
    const text = String(value);
    if (!text.includes("\x1b[")) return escapeHTML(text);

    const activeClasses = new Set();
    const pattern = /\x1b\[([0-9;]*)m/g;
    let html = "";
    let cursor = 0;
    let match;
    while ((match = pattern.exec(text)) !== null) {
      html += wrapTerminalSegment(text.slice(cursor, match.index), activeClasses);
      applyAnsiCodes(activeClasses, match[1]);
      cursor = pattern.lastIndex;
    }
    html += wrapTerminalSegment(text.slice(cursor), activeClasses);
    return html;
  }

  function wrapTerminalSegment(segment, activeClasses) {
    if (!segment) return "";
    const className = Array.from(activeClasses).join(" ");
    const escaped = escapeHTML(segment);
    return className ? `<span class="${className}">${escaped}</span>` : escaped;
  }

  function applyAnsiCodes(activeClasses, sequence) {
    const codes = sequence === "" ? [0] : sequence.split(";")
      .map((code) => Number.parseInt(code, 10))
      .filter((code) => !Number.isNaN(code));
    if (codes.length === 0) codes.push(0);

    for (let index = 0; index < codes.length; index += 1) {
      const code = codes[index];
      if (code === 0) {
        activeClasses.clear();
      } else if (code === 1) {
        activeClasses.add("ansi-bold");
      } else if (code === 2) {
        activeClasses.add("ansi-dim");
      } else if (code === 22) {
        activeClasses.delete("ansi-bold");
        activeClasses.delete("ansi-dim");
      } else if (code === 39) {
        removeAnsiColors(activeClasses);
      } else if (code >= 30 && code <= 37) {
        setAnsiColor(activeClasses, ansiColors[code - 30]);
      } else if (code >= 90 && code <= 97) {
        setAnsiColor(activeClasses, ansiColors[code - 90]);
      } else if (code === 38 || code === 48) {
        index = skipExtendedAnsiColor(codes, index);
      }
    }
  }

  function setAnsiColor(activeClasses, color) {
    removeAnsiColors(activeClasses);
    activeClasses.add(`ansi-${color}`);
  }

  function removeAnsiColors(activeClasses) {
    for (const color of ansiColors) activeClasses.delete(`ansi-${color}`);
  }

  function skipExtendedAnsiColor(codes, index) {
    if (codes[index + 1] === 5) return index + 2;
    if (codes[index + 1] === 2) return index + 4;
    return index;
  }

  function captureFocus() {
    const active = document.activeElement;
    if (!active || !active.matches("input, select, textarea, button, a[href]")) {
      restoreFocusSelector = undefined;
      return;
    }
    if (active.id) {
      restoreFocusSelector = `#${CSS.escape(active.id)}`;
    } else if (active.name) {
      restoreFocusSelector = `${active.tagName.toLowerCase()}[name="${CSS.escape(active.name)}"]`;
    } else {
      restoreFocusSelector = undefined;
    }
  }

  function restoreFocus() {
    if (!restoreFocusSelector) return;
    const next = document.querySelector(restoreFocusSelector);
    if (next) next.focus({ preventScroll: true });
    restoreFocusSelector = undefined;
  }

  function resetWorkbenchScroll(target) {
    if (!target || target.id !== "workbench") return;
    const scroller = target.closest(".workbench");
    if (!scroller) return;
    scroller.scrollTo({ top: 0, left: 0 });
  }

  function copyValue(value) {
    if (!value) return;
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(value).catch(() => fallbackCopy(value));
      return;
    }
    fallbackCopy(value);
  }

  function fallbackCopy(value) {
    const input = document.createElement("textarea");
    input.value = value;
    input.setAttribute("readonly", "readonly");
    input.style.position = "fixed";
    input.style.opacity = "0";
    document.body.appendChild(input);
    input.select();
    document.execCommand("copy");
    input.remove();
  }

  function isTypingTarget(target) {
    return target && target.closest && target.closest("input, textarea, select, [contenteditable='true']");
  }

  function ensureCommandPalette() {
    if (document.getElementById("command-palette")) return;
    const palette = document.createElement("div");
    palette.id = "command-palette";
    palette.className = "command-palette";
    palette.setAttribute("role", "dialog");
    palette.setAttribute("aria-label", "Command palette");
    palette.innerHTML = `
      <a href="/ui">Home</a>
      <a href="/setup">Setup Workbench</a>
      <a href="/ops/profiles">Profile Diagnostics</a>
    `;
    document.body.appendChild(palette);
  }

  function toggleCommandPalette(force) {
    ensureCommandPalette();
    const palette = document.getElementById("command-palette");
    const open = force === undefined ? !palette.classList.contains("open") : force;
    palette.classList.toggle("open", open);
    if (open) {
      const first = palette.querySelector("a");
      if (first) first.focus();
    }
  }

  function setCommandResults(input, open) {
    const results = document.querySelector("[data-command-results]");
    if (!results) return;
    results.classList.toggle("open", Boolean(open));
    if (input) {
      const bounds = input.getBoundingClientRect();
      if (bounds.width > 0) {
        results.style.right = "auto";
        results.style.left = `${Math.max(12, bounds.left)}px`;
        results.style.top = `${bounds.bottom + 8}px`;
        results.style.width = `${Math.min(680, Math.max(320, bounds.width))}px`;
      }
    }
  }

  function filterCommandItems(input) {
    const results = document.querySelector("[data-command-results]");
    if (!results) return;
    const query = String(input.value || "").trim().toLowerCase();
    for (const item of results.querySelectorAll("[data-command-item]")) {
      const haystack = item.textContent.toLowerCase();
      item.hidden = query !== "" && !haystack.includes(query);
    }
    setCommandResults(input, document.activeElement === input && (query !== "" || results.querySelector("[data-command-item]:not([hidden])")));
  }

  function openFirstCommand(input) {
    const results = document.querySelector("[data-command-results]");
    if (!results) return false;
    const first = results.querySelector("[data-command-item]:not([hidden])");
    if (!first) return false;
    window.location.assign(first.href);
    return true;
  }

  function focusCommandInput() {
    const input = document.querySelector("[data-command-input]");
    if (!input) return false;
    input.focus();
    input.select();
    filterCommandItems(input);
    setCommandResults(input, true);
    return true;
  }

  document.addEventListener("DOMContentLoaded", () => {
    connectRunStream();
    ensureCommandPalette();
  });
  document.body.addEventListener("htmx:beforeSwap", captureFocus);
  document.body.addEventListener("htmx:afterSwap", (event) => {
    connectRunStream();
    resetWorkbenchScroll(event.detail && event.detail.target);
    restoreFocus();
  });
  document.addEventListener("click", (event) => {
    const trigger = event.target.closest("[data-copy]");
    if (trigger) {
      event.preventDefault();
      copyValue(trigger.getAttribute("data-copy"));
      return;
    }
    const addResource = event.target.closest("[data-add-resource]");
    if (addResource) {
      event.preventDefault();
      addStackResourceRow(addResource);
      return;
    }
    const removeResource = event.target.closest("[data-remove-resource]");
    if (removeResource) {
      event.preventDefault();
      const row = removeResource.closest(".resource-row");
      if (row) row.remove();
      return;
    }
    const profileTab = event.target.closest("[data-profile-tab]");
    if (profileTab) {
      const tabs = profileTab.closest(".profile-tabs");
      if (tabs) {
        for (const tab of tabs.querySelectorAll("[data-profile-tab]")) tab.classList.remove("tab-active");
        profileTab.classList.add("tab-active");
      }
      return;
    }
    if (!event.target.closest("[data-command-input], [data-command-results]")) {
      setCommandResults(undefined, false);
    }
  });
  document.addEventListener("input", (event) => {
    const input = event.target.closest("[data-command-input]");
    if (input) filterCommandItems(input);
  });
  document.addEventListener("focusin", (event) => {
    const input = event.target.closest("[data-command-input]");
    if (input) filterCommandItems(input);
  });
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      setCommandResults(undefined, false);
      toggleCommandPalette(false);
      return;
    }
    const commandInput = event.target.closest && event.target.closest("[data-command-input]");
    if (commandInput && event.key === "Enter") {
      event.preventDefault();
      openFirstCommand(commandInput);
      return;
    }
    if (isTypingTarget(event.target)) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      if (focusCommandInput()) return;
      toggleCommandPalette();
      return;
    }
    if (event.key === "/") {
      const search = document.querySelector("[data-command-input]") || document.querySelector("input[name='q']");
      if (search) {
        event.preventDefault();
        search.focus();
        if (search.matches("[data-command-input]")) filterCommandItems(search);
      }
      return;
    }
    if (event.key.toLowerCase() === "o") {
      const link = document.querySelector("[data-command-link='ops']");
      if (link) window.location.assign(link.href);
    } else if (event.key.toLowerCase() === "s") {
      const link = document.querySelector("[data-command-link='setup']");
      if (link) window.location.assign(link.href);
    }
  });

  function addStackResourceRow(trigger) {
    const panel = trigger.closest(".ops-panel");
    if (!panel) return;
    const template = panel.querySelector("[data-resource-template]");
    const list = panel.querySelector("[data-resource-list]");
    if (!template || !list) return;
    const fragment = template.content ? template.content.cloneNode(true) : undefined;
    if (!fragment) return;
    list.appendChild(fragment);
    const lastRow = list.querySelector(".resource-row:last-child");
    if (lastRow) {
      const firstInput = lastRow.querySelector("input, select, textarea, button");
      if (firstInput) firstInput.focus();
    }
  }
})();
