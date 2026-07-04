(() => {
  let source;
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

  document.addEventListener("DOMContentLoaded", connectRunStream);
  document.body.addEventListener("htmx:afterSwap", connectRunStream);
})();
