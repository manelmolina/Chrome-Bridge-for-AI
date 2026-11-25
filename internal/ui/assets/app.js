(() => {
  const params = new URLSearchParams(window.location.search);
  const token = params.get("token") || "";

  const form = document.getElementById("connectForm");
  const hostInput = document.getElementById("host");
  const portInput = document.getElementById("port");
  const userInput = document.getElementById("username");
  const passInput = document.getElementById("password");
  const keyPathInput = document.getElementById("privateKeyPath");
  const keyFileInput = document.getElementById("keyFileInput");
  const browseKeyBtn = document.getElementById("browseKeyBtn");
  const detectedKeysRow = document.getElementById("detectedKeysRow");
  const loadedKeyBadge = document.getElementById("loadedKeyBadge");
  const keyPassInput = document.getElementById("keyPassphrase");

  const authTabButtons = document.querySelectorAll(".auth-tab[data-auth]");
  const passwordPanel = document.getElementById("passwordPanel");
  const keyPanel = document.getElementById("keyPanel");

  const connectBtn = document.getElementById("connectBtn");
  const disconnectBtn = document.getElementById("disconnectBtn");
  const togglePassBtn = document.getElementById("togglePassBtn");
  const toggleKeyPassBtn = document.getElementById("toggleKeyPassBtn");
  const formError = document.getElementById("formError");

  const statusBadge = document.getElementById("statusBadge");
  const chromeStatusText = document.getElementById("chromeStatusText");
  const chromeHelpBox = document.getElementById("chromeHelpBox");
  const refreshChromeBtn = document.getElementById("refreshChromeBtn");
  const copyInspectBtn = document.getElementById("copyInspectBtn");
  const launchDebugBtn = document.getElementById("launchDebugBtn");

  const logsBox = document.getElementById("logsBox");
  const copyLogsBtn = document.getElementById("copyLogsBtn");

  let lastLogs = [];
  let activeAuth = "password";
  let loadedKeyContent = "";

  async function api(path, options = {}) {
    const headers = Object.assign({ "X-App-Token": token }, options.headers || {});
    if (options.body && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    const resp = await fetch(path, Object.assign({}, options, { headers }));
    if (resp.status === 204) return null;
    const data = await resp.json().catch(() => ({}));
    if (!resp.ok) {
      throw new Error(data.error || `HTTP ${resp.status}`);
    }
    return data;
  }

  function showError(msg) {
    if (!msg) {
      formError.classList.add("hidden");
      formError.textContent = "";
      return;
    }
    formError.textContent = msg;
    formError.classList.remove("hidden");
  }

  function setAuthMode(mode) {
    activeAuth = mode === "key" ? "key" : "password";
    authTabButtons.forEach((btn) => {
      const isActive = btn.dataset.auth === activeAuth;
      btn.classList.toggle("active", isActive);
      btn.setAttribute("aria-selected", isActive ? "true" : "false");
    });
    const isKey = activeAuth === "key";
    passwordPanel.classList.toggle("hidden", isKey);
    keyPanel.classList.toggle("hidden", !isKey);
    passInput.required = !isKey;
    keyPathInput.required = isKey && !loadedKeyContent;
    showError("");
  }

  function setInputsDisabled(disabled) {
    [hostInput, portInput, userInput, passInput, keyPathInput, keyPassInput, browseKeyBtn].forEach((el) => {
      el.disabled = disabled;
    });
    authTabButtons.forEach((btn) => { btn.disabled = disabled; });
    detectedKeysRow.querySelectorAll("button").forEach((btn) => { btn.disabled = disabled; });
  }

  function renderStatus(snap) {
    statusBadge.className = "badge";
    switch (snap.state) {
      case "connecting":
        statusBadge.classList.add("badge-connecting");
        statusBadge.textContent = "Conectando...";
        break;
      case "waiting_chrome":
        statusBadge.classList.add("badge-waiting");
        statusBadge.textContent = "Esperando Chrome";
        break;
      case "active":
        statusBadge.classList.add("badge-active");
        statusBadge.textContent = "Túnel Activo";
        break;
      case "error":
        statusBadge.classList.add("badge-error");
        statusBadge.textContent = "Error";
        break;
      default:
        statusBadge.classList.add("badge-idle");
        statusBadge.textContent = "Desconectado";
    }

    const busy = snap.state === "connecting" || snap.state === "waiting_chrome" || snap.state === "active";
    setInputsDisabled(busy);
    connectBtn.classList.toggle("hidden", busy);
    disconnectBtn.classList.toggle("hidden", !busy);

    if (snap.state === "error" && snap.message) {
      showError(snap.message);
    } else if (busy) {
      showError("");
    }

    if (snap.localChrome) {
      chromeStatusText.className = "chrome-status ok";
      chromeStatusText.textContent = `Listo en 127.0.0.1:${snap.localChrome.port} (${snap.localChrome.sourceType})`;
      chromeHelpBox.classList.add("hidden");
    } else {
      chromeStatusText.className = "chrome-status warn";
      chromeStatusText.textContent = "Depuración remota no detectada";
      chromeHelpBox.classList.remove("hidden");
    }

    if (Array.isArray(snap.logs)) {
      lastLogs = snap.logs;
      const atBottom = logsBox.scrollTop + logsBox.clientHeight >= logsBox.scrollHeight - 20;
      logsBox.innerHTML = "";
      for (const entry of snap.logs) {
        const line = document.createElement("div");
        line.className = `log-line log-${entry.level || "info"}`;
        const t = document.createElement("span");
        t.className = "log-time";
        t.textContent = `[${entry.time}]`;
        const msg = document.createElement("span");
        msg.textContent = entry.message;
        line.appendChild(t);
        line.appendChild(msg);
        logsBox.appendChild(line);
      }
      if (atBottom) {
        logsBox.scrollTop = logsBox.scrollHeight;
      }
    }
  }

  authTabButtons.forEach((btn) => {
    btn.addEventListener("click", () => {
      if (!btn.disabled) {
        setAuthMode(btn.dataset.auth);
      }
    });
  });

  togglePassBtn.addEventListener("click", () => {
    passInput.type = passInput.type === "password" ? "text" : "password";
  });

  toggleKeyPassBtn.addEventListener("click", () => {
    keyPassInput.type = keyPassInput.type === "password" ? "text" : "password";
  });

  browseKeyBtn.addEventListener("click", () => {
    keyFileInput.value = "";
    keyFileInput.click();
  });

  keyFileInput.addEventListener("change", async () => {
    const file = keyFileInput.files && keyFileInput.files[0];
    if (!file) return;
    try {
      loadedKeyContent = await file.text();
      keyPathInput.value = file.name;
      keyPathInput.required = false;
      loadedKeyBadge.textContent = `Archivo cargado en memoria: ${file.name}`;
      loadedKeyBadge.classList.remove("hidden");
      showError("");
    } catch {
      showError("No se pudo leer el archivo seleccionado.");
    }
  });

  keyPathInput.addEventListener("input", () => {
    loadedKeyContent = "";
    loadedKeyBadge.classList.add("hidden");
    keyPathInput.required = activeAuth === "key";
  });

  refreshChromeBtn.addEventListener("click", async () => {
    await api("/api/chrome/refresh", { method: "POST" }).catch(() => {});
  });

  copyInspectBtn.addEventListener("click", async () => {
    const text = "chrome://inspect/#remote-debugging";
    try {
      await navigator.clipboard.writeText(text);
      const orig = copyInspectBtn.textContent;
      copyInspectBtn.textContent = "¡Copiado! Pégalo en Chrome";
      setTimeout(() => { copyInspectBtn.textContent = orig; }, 2200);
    } catch {
      window.prompt("Copia esta URL y ábrela en Chrome:", text);
    }
  });

  launchDebugBtn.addEventListener("click", async () => {
    showError("");
    try {
      await api("/api/chrome/launch-debug", { method: "POST" });
    } catch (err) {
      showError(err.message);
    }
  });

  copyLogsBtn.addEventListener("click", async () => {
    const txt = lastLogs.map((l) => `[${l.time}] [${l.level}] ${l.message}`).join("\n");
    try {
      await navigator.clipboard.writeText(txt);
      const orig = copyLogsBtn.textContent;
      copyLogsBtn.textContent = "¡Copiado!";
      setTimeout(() => { copyLogsBtn.textContent = orig; }, 1800);
    } catch {}
  });

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    showError("");
    try {
      const payload = {
        host: hostInput.value.trim(),
        port: parseInt(portInput.value, 10) || 22,
        username: userInput.value.trim(),
        authType: activeAuth,
        standardCdpPort: 9222,
        fallbackDebugPort: 9222,
      };
      if (activeAuth === "key") {
        payload.privateKeyPath = loadedKeyContent ? "" : keyPathInput.value.trim();
        payload.privateKeyContent = loadedKeyContent;
        payload.keyPassphrase = keyPassInput.value;
      } else {
        payload.password = passInput.value;
      }
      await api("/api/connect", {
        method: "POST",
        body: JSON.stringify(payload),
      });
    } catch (err) {
      showError(err.message);
    }
  });

  disconnectBtn.addEventListener("click", async () => {
    try {
      await api("/api/disconnect", { method: "POST" });
    } catch (err) {
      showError(err.message);
    }
  });

  (async () => {
    try {
      const prof = await api("/api/profile");
      if (prof.host) hostInput.value = prof.host;
      if (prof.port) portInput.value = prof.port;
      if (prof.username) userInput.value = prof.username;
      if (prof.privateKeyPath) keyPathInput.value = prof.privateKeyPath;
      setAuthMode(prof.authType === "key" ? "key" : "password");
    } catch {
      setAuthMode("password");
    }

    try {
      const res = await api("/api/ssh-keys");
      if (Array.isArray(res.keys) && res.keys.length > 0) {
        detectedKeysRow.innerHTML = "";
        res.keys.forEach((kPath) => {
          const chip = document.createElement("button");
          chip.type = "button";
          chip.className = "key-chip";
          const parts = kPath.split(/[/\\]/);
          chip.textContent = parts[parts.length - 1] || kPath;
          chip.title = kPath;
          chip.addEventListener("click", () => {
            loadedKeyContent = "";
            loadedKeyBadge.classList.add("hidden");
            keyPathInput.value = kPath;
            keyPathInput.required = activeAuth === "key";
            showError("");
          });
          detectedKeysRow.appendChild(chip);
        });
        detectedKeysRow.classList.remove("hidden");
      }
    } catch {}

    try {
      const snap = await api("/api/status");
      renderStatus(snap);
    } catch {}

    const es = new EventSource(`/api/events?token=${encodeURIComponent(token)}`);
    es.onmessage = (ev) => {
      try {
        renderStatus(JSON.parse(ev.data));
      } catch {}
    };
  })();

  window.addEventListener("beforeunload", () => {
    navigator.sendBeacon(`/api/window-closed?token=${encodeURIComponent(token)}`);
  });
})();
