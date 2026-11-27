<div align="center">

# Diana

[简体中文](./README.md) · [English](./README.en.md) · [日本語](./README.ja.md) · [한국어](./README.ko.md) · [Français](./README.fr.md) · **Deutsch** · [Nederlands](./README.nl.md)

**Schreibt wie ein Mensch, arbeitet wie ein Agent.**

Ein selbstgehosteter Single-Binary-KI-Agent — erkennt in Gruppenchats von selbst, wann er sich einbringen und wann er still bleiben sollte, ganz ohne `@`-Erwähnung; beherrscht Langzeitgedächtnis, Bilderkennung, Link- und Dateianalyse, Musikwünsche, Websuche, Skills / MCP und lokale Coding-Agents. Alle Daten und Entscheidungen bleiben auf deinem Rechner.

<sub>OneBot v11 · Telegram · QQ Official Bot · WeChat · iMessage · Integrierte Konsole — eine Instanz, alle gleichzeitig online</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website](https://suink.github.io/Diana/de/) · [Dokumentation (EN)](https://suink.github.io/Diana/en/deploy.html) · [Live-Demo](https://suink.github.io/Diana/demo/) · [Neuestes Release](https://github.com/SuInk/Diana/releases/latest)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana-Konsolenübersicht: Kanalstatus, Nachrichtenstatistiken, Systemressourcen und Live-Ereignisstrom" width="100%" />

## Warum Diana

Die meisten selbstgehosteten Chatbots landen in einem von zwei Extremen: entweder ein Spielzeug, das ohne `@`-Erwähnung stumm bleibt (oder bei proaktiven Antworten sofort spammt), oder ein Geflecht aus Python-/Node-Umgebungen und YAML-Dateien, das irgendwann einfach nicht mehr antwortet — ohne jede Erklärung.

Diana vereint natürliche Gesprächspräsenz und echte Agenten-Fähigkeiten in einer einzigen Go-Binärdatei: Auf der einen Seite angebunden an gängige LLMs (OpenAI-kompatible APIs, Gemini, Anthropic), auf der anderen Seite gleichzeitig in deinen täglichen Messengern aktiv — gesteuert über eine integrierte Web-Konsole.

- **Weiß, wann es reden — und wann es schweigen sollte** — meldet sich in Gruppen ohne `@` im richtigen Moment zu Wort, merkt sich Regeln und Absprachen über geschichtetes Gedächtnis und ein bearbeitbares Notizbuch und verarbeitet Bilder, Videolinks, Musikwünsche und Sticker.
- **Erledigt echte Arbeit direkt im Chat** — Websuche, Dokumentenanalyse und gerenderte Diagramme sind ab Werk dabei; der integrierte Agent nutzt Skills, MCP, Browser-Tools und delegiert auf Wunsch an lokales Claude Code / Codex.
- **In 3 Minuten startklar, keine Blackbox** — ein Befehl zur Installation, ausgehende Verbindungen ohne öffentliche IP, Einrichtung im Browser; im Ereigniscenter siehst du bei jeder Nachricht, warum sie beantwortet oder übersprungen wurde und wie viele Token sie gekostet hat.
- **Deine Daten, klare Sicherheitsgrenzen** — Konfiguration, Gedächtnis und Logs liegen lokal in SQLite ohne Cloud-Abhängigkeit; der **Sicherheitsmodus** sperrt Host-Befehle, Schreibzugriffe und Coding-Agents mit einem Schalter gegen Prompt-Injection.

## In drei Minuten einsatzbereit

**① Installieren.** Einen Befehl kopieren und ausführen — das Skript lädt die aktuelle stabile Version herunter, prüft SHA-256 und startet Diana:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# Android Termux (nur ARM64; in Termux ausführen, kein Root nötig)
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Oder per Docker (im gewünschten Datenverzeichnis ausführen)
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

Der native Installer benötigt keine Admin-Rechte. **Für Upgrades einfach denselben Befehl erneut ausführen.** Mehr Optionen im [Deployment-Guide](https://suink.github.io/Diana/en/deploy.html).

**Oder per Skill durch einen KI-Assistenten installieren lassen.** Kopiere diese Anweisung in einen Assistenten mit Terminalzugriff (die vollständige Anleitung liegt im [Install-Skill](skills/diana-install/SKILL.md)):

```text
Read https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md and follow it to install the latest stable release of Diana on this machine.
```

**Oder manuell herunterladen** (immer die neueste stabile Version):

| Plattform | Download | Starten |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | In einen eigenen Ordner legen und doppelklicken (Daten in `data`) |
| macOS Apple Silicon | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | Entpacken und `./run.sh` ausführen |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | Entpacken und `./run.sh` ausführen |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | Entpacken und `./run.sh` ausführen |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | Entpacken und `./run.sh` ausführen |
| Android (Termux) | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | In Termux entpacken und `./run.sh` ausführen (Server, keine Android-App) |

Die WebUI ist direkt in die Binärdatei eingebettet — Node.js ist nicht erforderlich. Alle SHA-256-Prüfsummen stehen in [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json) und auf [Releases](https://github.com/SuInk/Diana/releases).

**② In der Konsole anmelden.** Öffne `http://127.0.0.1:18080`. Benutzername und Passwort des ersten Admins werden einmalig im Terminal ausgegeben (bei Docker: `docker logs diana`) — sofort sichern. Vergessen? Einfach `diana passwd` ausführen.

**③ Konfigurieren.** Drei Schritte in der Konsole:

1. Unter **LLM** den API-Key eintragen, Modelle synchronisieren und ein Standardmodell wählen.
2. Unter **Bots** eine Plattform auswählen und Zugangsdaten eingeben (oder ohne jede Plattform direkt mit dem **integrierten Bot** testen).
3. Eine Nachricht senden — Direktnachrichten werden sofort beantwortet, in Gruppen per `@`-Erwähnung oder Auslösewort.

> [!TIP]
> **Konsole von einem anderen Gerät öffnen:** Standardmäßig lauscht Diana nur auf `127.0.0.1`. Installer mit `-s -- --host 0.0.0.0` (Windows: `-BindHost 0.0.0.0`) erneut ausführen und im Internet immer hinter einen HTTPS-Reverse-Proxy stellen.

## Unterstützte Plattformen

Alle aktivierten Bot-Profile sind gleichzeitig online; Antworten gehen stets in den Ursprungskanal zurück. Heim- und lokale Netzwerke funktionieren ohne öffentliche IP.

| Plattform | Benötigte Zugangsdaten | Verbindungsrichtung |
| --- | --- | --- |
| **OneBot v11** (SnowLuma empfohlen) | Reverse WS, Forward WS oder HTTP API + Event-Push (auf der Bot-Seite wählbar) | Je nach Modus; LAN-tauglich |
| **Telegram** | Bot-Token von BotFather | Ausgehend von Diana (keine öffentliche IP nötig) |
| **QQ Official Bot** | AppID + AppSecret der QQ Open Platform | Ausgehend von Diana (keine öffentliche IP nötig) |
| **WeChat** | Keine Eingabe nötig: Bot speichern und QR-Code scannen (Tencent iLink Bot, nur Privat-Chats) | Ausgehend von Diana (keine öffentliche IP nötig) |
| **iMessage** | Adresse und Passwort von BlueBubbles Server auf einem Mac ([Anleitung](docs/imessage.md)) | BlueBubbles-Webhook → Diana (Polling als Fallback) |
| **Integrierter Bot** | Keine: direkt im Chat-Tab der Konsole schreiben oder Sprachanrufe führen | Nur Konsole; sofortiger Einstieg |

## Integrierte Fähigkeiten

Alles ist direkt in der einzelnen Binärdatei enthalten und lässt sich in der Konsole ohne Plugins ein- und ausschalten:

- **Proaktive Teilnahme** — entscheidet selbstständig, wann es sich im Gruppenchat einbringt und wann es still bleibt; jede Entscheidung wird im Ereigniscenter protokolliert.
- **Langzeitgedächtnis & Notizbuch** — geschichtetes Gedächtnis hält die Token-Kosten niedrig; wichtige Fakten landen in einem prüfbaren, bearbeitbaren Notizbuch.
- **Integrierter Agent & Sicherheitsmodus** — Tool-Schleife mit Datei-, Befehls- und Browser-Werkzeugen sowie Skills und MCP-Servern bei Bedarf; der **Sicherheitsmodus** sperrt pro Bot sensible Host-Werkzeuge gegen Prompt-Injection.
- **Coding-Agent** — nur für Besitzer, standardmäßig aus: delegiert längere Code-Änderungen und Tests in registrierten Repos an lokales Claude Code / Codex CLI, übersteht Neustarts und fragt vor riskanten Aktionen im Chat nach Bestätigung.
- **Websuche** — recherchiert zeitkritische Fragen standardmäßig parallel über einen isolierten Browser oder über global konfigurierte Anbieter (Exa, Tavily u. a.).
- **Link- & Dateianalyse** — wertet Medien und Profile von Bilibili / YouTube / X / Xiaohongshu / Douyin sowie PDF-, Office- und EPUB-Dateien direkt aus.
- **Vision, OCR & gerenderte Grafiken** — Vision-Modelle plus Offline-OCR lassen auch reine Textmodelle Bilder verstehen; Tabellen und Diagramme (Markdown / Mermaid / SVG) werden automatisch als Bild gerendert.
- **Musik, Sticker & Gruppenregeln** — Musikwünsche aus mehreren Quellen, passende Sticker aus dem Chat und gruppenindividuelle Antwortzeiten, Personas, Listen und Tool-Rechte.

Vollständige Referenz in der [Konfigurationsdokumentation](https://suink.github.io/Diana/en/configuration.html).

## Täglicher Betrieb

Nach der Installation steht `diana` direkt im PATH bereit:

```sh
diana status    # Status, Version, Adresse
diana logs -f   # Logs live verfolgen
diana restart   # Dienst neu starten
diana doctor    # Konfiguration, Verzeichnisse und Gesundheit prüfen
```

Bei Docker auf dem Host `docker exec diana diana <Befehl>` verwenden.

- **Passwort vergessen**: `diana passwd` (nativ) bzw. `docker compose stop diana && docker compose run --rm diana passwd && docker compose start diana` (Docker).
- **Upgrade**: Installationsbefehl erneut ausführen oder in der Konsole aktualisieren; inklusive automatischem Daten-Backup und Rollback bei fehlgeschlagenem Health-Check.
- **Deinstallation**: `diana uninstall` entfernt Programm und Dienst und behält die Daten; `diana uninstall --purge` löscht auch alle Daten unwiderruflich.

## Sicherheitshinweise

- Login-Pflicht ab dem ersten Start, mit IP-basiertem Rate-Limiting gegen Brute-Force.
- Gespeicherte API-Keys und Tokens werden nie wieder im Klartext ausgeliefert.
- Hinter einem Reverse-Proxy unbedingt `trusted_proxies` in `config.yaml` (oder `DIANA_TRUSTED_PROXIES`) konfigurieren.
- Mit hinterlegtem Besitzerkonto ermöglicht der 6-stellige Einmalcode per Bot-DM einen schnellen Login inklusive sofortiger Rückmeldung von IP und Gerät.

## Mitwirken

Backend in Go (Gin + SQLite), Frontend in Vue 3 + TypeScript (`frontend/`).

```sh
make dev        # Backend :18080 + Vite :5173
go test ./...   # Backend-Tests
```

PRs sind herzlich willkommen! Details in [CONTRIBUTING.md](./CONTRIBUTING.md) und [AGENTS.md](./AGENTS.md).

## Dokumentation

| | |
| --- | --- |
| [Deployment](https://suink.github.io/Diana/en/deploy.html) | Installationswege, Serverbetrieb, erster Login |
| [Konfiguration](https://suink.github.io/Diana/en/configuration.html) | Kanäle, Modell-Routing, Gruppenregeln, Sicherheitsgrenzen |
| [Implementierung](https://suink.github.io/Diana/en/implementation.html) | Architektur, Entscheidungspipeline, Gedächtnisschichten |
| [Pipeline](https://suink.github.io/Diana/en/pipeline.html) | Flussdiagramme für Trigger, Agent-Läufe, Browser und Prüfung |
| [Betrieb](https://suink.github.io/Diana/en/operations.html) | Updates, Backups, Fehlerbehebung |
| [Live-Demo](https://suink.github.io/Diana/demo/) | Die echte Konsole mit Beispieldaten |

## Lizenz

`Limited Redistribution License (SuInk)` — siehe [LICENSE](./LICENSE).
