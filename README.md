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

Para usarlo, solo tienes que decirle a tu agente de IA que se conecte a Chrome por Chrome Dev Tools y revise lo que necesites (por ejemplo: *"Conéctate a Chrome por Chrome Dev Tools y mira los errores de consola o las peticiones de red"*).

## Compilar todo desde Linux

```bash
chmod +x build.sh
./build.sh
```
