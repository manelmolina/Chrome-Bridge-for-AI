# Chrome Bridge for AI

Aplicación de escritorio ligera para **Windows**, **Linux (AppImage)** y **macOS** pensada exclusivamente para hacer **troubleshooting y debugging de aplicaciones**, permitiendo que un agente de IA en tu servidor (**Antigravity**, **Codex CLI**, **Claude Code**, **Playwright**, **Puppeteer** y cualquier cliente MCP/CDP) pueda ver toda la información de tu navegador local (consola, red, DOM, capturas de pantalla, estado e interacción) a través de un túnel SSH inverso seguro.

> **Uso previsto:** esta herramienta sirve únicamente para tareas de troubleshooting y debugging de aplicaciones donde necesites que tu agente de IA inspeccione y vea todo lo que ocurre en tu navegador.

## Descargas generadas (`dist/`)

- **Linux (AppImage 64-bit)**: `dist/Chrome-Bridge-for-AI-x86_64.AppImage`
- **Windows (64-bit)**: `dist/chrome-bridge-for-ai-windows-amd64.exe`
- **macOS Apple Silicon (M1/M2/M3/M4)**: `dist/chrome-bridge-for-ai-macos-apple-silicon-arm64`
- **macOS Intel**: `dist/chrome-bridge-for-ai-macos-intel-amd64`

## Qué expone en el servidor al conectar

Al establecer el túnel SSH, la app habilita automáticamente **ambos** mecanismos en el servidor:
1. **`DevToolsActivePort` (`~/.config/google-chrome/DevToolsActivePort`)** + puerto inverso hacia tu Chrome local: compatible de serie con **Antigravity (`/browser`)** y clientes `--autoConnect`.
2. **Endpoint CDP estándar (`http://127.0.0.1:9222`)**: proxy HTTP+WebSocket compatible con **Codex CLI**, **Claude Code**, **Playwright**, **Puppeteer** y `chrome-devtools-mcp --browser-url=http://127.0.0.1:9222`.

## Uso rápido

1. Ejecuta la versión de tu sistema:
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
2. Dentro de la aplicación, pulsa **Abrir Chrome aislado (:9222)** para lanzar un perfil de Chrome con CDP ya activo.
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
