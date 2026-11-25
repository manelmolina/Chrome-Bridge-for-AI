# Chrome Bridge for AI

Aplicación de escritorio ligera para **Windows**, **Linux (AppImage)** y **macOS** que crea un túnel SSH inverso seguro desde tu servidor hacia tu Google Chrome local, permitiendo que **Antigravity**, **Codex CLI**, **Claude Code**, **Playwright**, **Puppeteer** y cualquier cliente MCP/CDP en el servidor controlen tu navegador local (capturas de pantalla, logs de consola, red, interacción, etc.).

## Descargas generadas (`dist/`)

- **Linux (AppImage 64-bit)**: `dist/Chrome-Bridge-for-AI-x86_64.AppImage`
- **Windows (64-bit)**: `dist/chrome-bridge-for-ai-windows-amd64.exe`
- **macOS Apple Silicon (M1/M2/M3/M4)**: `dist/chrome-bridge-for-ai-macos-apple-silicon-arm64`
- **macOS Intel**: `dist/chrome-bridge-for-ai-macos-intel-amd64`

## Qué expone en el servidor al conectar

Al establecer el túnel SSH, la app habilita automáticamente **ambos** mecanismos en el servidor:
1. **`DevToolsActivePort` (`~/.config/google-chrome/DevToolsActivePort`)** + puerto inverso hacia tu Chrome local: compatible de serie con **Antigravity (`/browser`)** y clientes `--autoConnect`.
2. **Endpoint CDP estándar (`http://127.0.0.1:9222`)**: proxy HTTP+WebSocket compatible con **Codex CLI**, **Claude Code**, **Playwright**, **Puppeteer** y `chrome-devtools-mcp --browser-url=http://127.0.0.1:9222`, incluso si Chrome se abrió mediante `chrome://inspect/#remote-debugging`.

## Uso rápido

1. En tu PC (Windows, Linux o macOS), abre Google Chrome (`v144+`), entra en:
   ```text
   chrome://inspect/#remote-debugging
   ```
   y activa **Allow remote debugging for this browser instance**.
   *(También puedes pulsar **Abrir Chrome aislado (:9222)** dentro de la app).*
2. Ejecuta la versión de tu sistema:
   - **Linux (AppImage)**:
     ```bash
     chmod +x Chrome-Bridge-for-AI-x86_64.AppImage
     ./Chrome-Bridge-for-AI-x86_64.AppImage
     ```
   - **Windows**: doble clic en `chrome-bridge-for-ai-windows-amd64.exe`.
   - **macOS**: abre el ejecutable de tu procesador (`chrome-bridge-for-ai-macos-apple-silicon-arm64` o `chrome-bridge-for-ai-macos-intel-amd64`). Si lo ejecutas desde Terminal o macOS lo bloquea por no estar firmado, ejecuta una vez:
     ```bash
     chmod +x chrome-bridge-for-ai-macos-apple-silicon-arm64
     xattr -d com.apple.quarantine chrome-bridge-for-ai-macos-apple-silicon-arm64 2>/dev/null || true
     ./chrome-bridge-for-ai-macos-apple-silicon-arm64
     ```
     *(Cambia `apple-silicon-arm64` por `intel-amd64` si tu Mac es Intel).*
3. Introduce **IP/dominio del servidor**, **Puerto SSH** (`22` por defecto), **Usuario**, elige **Contraseña** o **Clave SSH**, y pulsa **Conectar y crear túnel**.

## Configurar herramientas en el servidor

- **Antigravity (`agy`)**: funciona directamente sin configuración extra usando `/browser ...`.
- **Codex CLI**:
  ```bash
  codex mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest --browser-url=http://127.0.0.1:9222
  ```
- **Claude Code**:
  ```bash
  claude mcp add chrome-devtools -- npx -y chrome-devtools-mcp@latest --browser-url=http://127.0.0.1:9222
  ```
- **Playwright MCP**:
  ```bash
  npx -y @playwright/mcp@latest --cdp-endpoint http://127.0.0.1:9222
  ```

## Compilar todo desde Linux

```bash
chmod +x build.sh
./build.sh
```
