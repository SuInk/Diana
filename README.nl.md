<div align="center">

# Diana

[简体中文](./README.md) · [English](./README.en.md) · [日本語](./README.ja.md) · [한국어](./README.ko.md) · [Français](./README.fr.md) · [Deutsch](./README.de.md) · **Nederlands**

**Praat als een mens, werkt als een agent.**

Een zelfgehoste single-binary AI-agent — weet in groepschats vanzelf wanneer mee te praten en wanneer stil te blijven zonder `@`-vermelding; regelt geheugen, beeldherkenning, links, muziek, webzoekopdrachten, Skills / MCP en lokale coding-agents, terwijl alle data en beslissingen op je eigen machine blijven.

<sub>OneBot v11 · Telegram · QQ Official Bot · WeChat · iMessage · Ingebouwde console — één instantie, alles tegelijk online</sub>

[![CI](https://github.com/SuInk/Diana/actions/workflows/ci.yml/badge.svg)](https://github.com/SuInk/Diana/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/SuInk/Diana?color=159f6c)](https://github.com/SuInk/Diana/releases/latest)
[![Go](https://img.shields.io/badge/Go-1.25%2B-00ADD8)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Limited%20Redistribution-informational)](./LICENSE)

[Website](https://suink.github.io/Diana/nl/) · [Documentatie (EN)](https://suink.github.io/Diana/en/deploy.html) · [Live demo](https://suink.github.io/Diana/demo/) · [Nieuwste release](https://github.com/SuInk/Diana/releases/latest)

[![QQ Group 1127671893](https://img.shields.io/badge/QQ%20Group-1127671893-12B7F5?logo=qq&logoColor=white)](https://qm.qq.com/q/4o6zEBs4h2)

</div>

<br />

<img src="./docs/assets/diana-webui-overview-green.jpg" alt="Diana-consoleoverzicht: kanaalstatus, berichtstatistieken, systeembronnen en realtime gebeurtenissen" width="100%" />

## Waarom Diana

De meeste zelfgehoste chatbots belanden in één van twee uitersten: een speeltje dat stil blijft tot je het `@`-vermeldt (of meteen spamt zodra proactieve antwoorden aanstaan), of een wirwar van Python/Node-omgevingen en YAML-bestanden die zonder uitleg stopt met reageren.

Diana combineert natuurlijke gespreksaanwezigheid en volwaardige agent-mogelijkheden in één Go-binary: aan de ene kant gekoppeld aan grote LLM's (OpenAI-compatibele API's, Gemini, Anthropic), aan de andere kant tegelijk actief in je dagelijkse chatapps — met een ingebouwde webconsole voor het volledige beheer.

- **Weet wanneer te praten — en wanneer te zwijgen** — haakt natuurlijk aan in groepsgesprekken zonder `@`, onthoudt groepsafspraken via gelaagd geheugen en een bewerkbaar notitieboek, en verwerkt afbeeldingen, videolinks, muziekverzoeken en stickers.
- **Verzet echt werk direct vanuit de chat** — webzoekopdrachten, bestandsanalyse en diagramrendering standaard ingebouwd; de agent laadt Skills, MCP en browsertools en kan opdrachten doorgeven aan lokale Claude Code / Codex.
- **In 3 minuten actief, geen zwarte doos** — installeren met één commando, uitgaande verbindingen zonder publiek IP-adres, instellen via de browser; het gebeurteniscentrum legt bij elk bericht uit waarom het wel of niet beantwoord is en hoeveel tokens het kostte.
- **Jouw data, heldere beveiligingsgrenzen** — configuratie, geheugen en logs staan lokaal in SQLite zonder cloudafhankelijkheid; de **Veilige modus** schakelt hostcommando's, bestandsschrijving en coding-agents met één klik uit tegen prompt-injectie.

## Binnen drie minuten aan de slag

**① Installeren.** Voer één commando uit — het script downloadt de nieuwste stabiele versie, controleert SHA-256 en start Diana:

```sh
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```sh
# Android Termux (alleen ARM64; uitvoeren in Termux, geen root nodig)
pkg install -y curl tar coreutils && curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.sh | sh
```

```powershell
# Windows PowerShell
irm https://raw.githubusercontent.com/SuInk/Diana/main/scripts/install.ps1 | iex
```

```sh
# Of met Docker (uitvoeren in de gewenste datamap)
curl -fsSL https://raw.githubusercontent.com/SuInk/Diana/main/scripts/docker.sh | sh
```

De native installer vereist geen beheerdersrechten. **Upgraden doe je door hetzelfde commando opnieuw te draaien.** Zie de [installatiegids](https://suink.github.io/Diana/en/deploy.html) voor meer opties.

**Of laat een AI-assistent installeren via de Skill.** Plak deze opdracht in een assistent die commando's kan uitvoeren (volledige stappen in de [installatie-skill](skills/diana-install/SKILL.md)):

```text
Read https://raw.githubusercontent.com/SuInk/Diana/main/skills/diana-install/SKILL.md and follow it to install the latest stable release of Diana on this machine.
```

**Of handmatig downloaden** (altijd de nieuwste stabiele release):

| Platform | Download | Uitvoeren |
| --- | --- | --- |
| Windows x64 | [diana-windows-amd64.exe](https://github.com/SuInk/Diana/releases/latest/download/diana-windows-amd64.exe) | Plaats in een eigen map en dubbelklik (data komt in `data`) |
| macOS Apple Silicon | [diana-macos-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-arm64.tar.gz) | Uitpakken en `./run.sh` starten |
| macOS Intel | [diana-macos-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-macos-amd64.tar.gz) | Uitpakken en `./run.sh` starten |
| Linux x64 | [diana-linux-amd64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-amd64.tar.gz) | Uitpakken en `./run.sh` starten |
| Linux ARM64 | [diana-linux-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-linux-arm64.tar.gz) | Uitpakken en `./run.sh` starten |
| Android (Termux) | [diana-android-arm64.tar.gz](https://github.com/SuInk/Diana/releases/latest/download/diana-android-arm64.tar.gz) | Uitpakken in Termux en `./run.sh` starten (server, geen Android-app) |

De WebUI is ingebouwd in het programma; Node.js is niet nodig. Alle SHA-256-hashes staan in [`latest.json`](https://github.com/SuInk/Diana/releases/latest/download/latest.json) en op [Releases](https://github.com/SuInk/Diana/releases).

**② Inloggen in de console.** Open `http://127.0.0.1:18080`. De gegenereerde beheerdersnaam en het wachtwoord verschijnen eenmalig in de terminal (bij Docker via `docker logs diana`) — bewaar ze direct. Vergeten? Reset ze met `diana passwd`.

**③ Configureren.** Drie stappen in de console:

1. Vul bij **LLM** je API-sleutel in, synchroniseer de modellen en kies een standaardmodel.
2. Kies bij **Bots** een platform en vul de gegevens in (of start direct met de **Ingebouwde bot** zonder externe koppeling).
3. Stuur een bericht — privéberichten worden direct beantwoord, in groepen met `@`-vermelding of triggerwoord.

> [!TIP]
> **Console openen vanaf een ander apparaat:** standaard luistert Diana alleen op `127.0.0.1`. Draai de installer opnieuw met `-s -- --host 0.0.0.0` (Windows: `-BindHost 0.0.0.0`) en plaats bij publieke toegang altijd een HTTPS reverse proxy ervoor.

## Ondersteunde platforms

Alle ingeschakelde botprofielen zijn tegelijk online en antwoorden gaan altijd terug naar het oorspronkelijke kanaal. Thuisnetwerken en LAN's werken zonder publiek IP.

| Platform | Benodigde gegevens | Verbindingsrichting |
| --- | --- | --- |
| **OneBot v11** (SnowLuma aanbevolen) | Reverse WS, Forward WS of HTTP API + events (te kiezen op de botpagina) | Afhankelijk van modus; werkt in LAN |
| **Telegram** | Bot Token van BotFather | Uitgaand vanaf Diana (geen publiek IP nodig) |
| **QQ Official Bot** | AppID + AppSecret van het QQ Open Platform | Uitgaand vanaf Diana (geen publiek IP nodig) |
| **WeChat** | Niets in te vullen: sla de bot op en scan de QR-code (Tencent iLink Bot, alleen privé) | Uitgaand vanaf Diana (geen publiek IP nodig) |
| **iMessage** | Adres en wachtwoord van BlueBubbles Server op een Mac ([handleiding](docs/imessage.md)) | BlueBubbles-webhook → Diana (polling als fallback) |
| **Ingebouwde bot** | Niets nodig: chat of bel met spraak direct op de Chat-pagina in de console | Alleen console; direct uit te proberen |

## Ingebouwde mogelijkheden

Standaard meegeleverd in de enkele binary en per functie in de console in te schakelen, zonder plugins:

- **Proactief meedoen** — bepaalt zelf wanneer aan te haken en wanneer stil te blijven in groepschats; elke beslissing staat gelogd in het gebeurteniscentrum.
- **Langetermijngeheugen & notitieboek** — gelaagd geheugen houdt tokenverbruik laag; belangrijke feiten staan in een controleerbaar, bewerkbaar notitieboek.
- **Ingebouwde agent & Veilige modus** — tool-loop met bestands-, commando- en browsertools plus Skills en MCP-servers op aanvraag; de **Veilige modus** blokkeert per bot gevoelige hosttools tegen prompt-injectie.
- **Coding-agent** — alleen voor eigenaar, standaard uit: laat lokale Claude Code / Codex CLI langlopende codewijzigingen en tests uitvoeren in geregistreerde repo's, blijft actief na herstarts en vraagt in de chat bevestiging vóór risicovolle stappen.
- **Webzoekopdrachten** — zoekt actuele informatie standaard parallel op via een geïsoleerde browser, met globaal instelbare providers (Exa, Tavily e.a.).
- **Link- & bestandsanalyse** — verwerkt media en profielen van Bilibili / YouTube / X / Xiaohongshu / Douyin en leest PDF-, Office- en EPUB-bestanden.
- **Vision, OCR & gerenderde afbeeldingen** — visionmodellen + offline OCR laten ook tekstmodellen afbeeldingen lezen; tabellen en diagrammen (Markdown / Mermaid / SVG) worden automatisch als afbeelding gerenderd.
- **Muziek, stickers & groepsbeleid** — muziekverzoeken uit meerdere bronnen, passende stickers uit de chat en per groep eigen antwoordtijden, persona's, lijsten en toolrechten.

Volledige uitleg in de [configuratiedocumentatie](https://suink.github.io/Diana/en/configuration.html).

## Dagelijks beheer

Na installatie staat `diana` direct in je PATH:

```sh
diana status    # status, versie, adres
diana logs -f   # logs live volgen
diana restart   # service herstarten
diana doctor    # controle van configuratie, mappen en status
```

Bij Docker voer je op de host `docker exec diana diana <commando>` uit.

**Wachtwoord vergeten**: het wachtwoord staat alleen als hash in de database en is dus nergens terug te halen — reset het in plaats daarvan. Bij een installatie met één klik (Linux / macOS / Windows) volstaat:

```sh
diana passwd
```

Het commando stopt de service, maakt een nieuw willekeurig wachtwoord aan dat één keer wordt getoond en start de service weer; alle browsersessies worden uitgelogd, bots, modellen, plugins en alle andere data blijven ongemoeid. Voor elke wijziging wordt om `y` gevraagd; `-y` slaat die vraag over (verplicht zonder terminal, bijvoorbeeld in scripts). Voeg `--username NIEUWE_NAAM` toe om het account tegelijk te hernoemen.

Docker kan zichzelf niet van binnen de container stoppen; voer daarom in de deploymentmap uit:

```sh
docker compose stop diana
docker compose run --rm diana passwd
docker compose start diana
```

Een proces dat je zelf hebt gestart (`run.sh`, of dubbelklikken op de .exe) wordt niet aangeraakt: stop het eerst zelf en voer dan `diana passwd` uit.

**Upgraden**: volledige Release-pakketten werk je bij door het installatiecommando opnieuw te draaien of in de console op upgraden te klikken; beide routes maken een back-up, controleren het pakket en draaien terug als de gezondheidscontrole mislukt. Docker-deployments halen het image op en maken de container opnieuw aan, op de host of via de optionele updater. Een Docker-image-update wordt niet automatisch teruggedraaid als de gezondheidscontrole mislukt; zet zo nodig op de host een bekende versietag terug.

**Verwijderen**: `diana uninstall` verwijdert service en programma maar bewaart je data (na een herinstallatie ga je verder waar je was); `diana uninstall --purge` wist ook de data — onomkeerbaar, met een tweede bevestiging.

## Waar de instellingen staan

Eén vuistregel: **dagelijkse instellingen wijzig je in de webconsole; `config.yaml` gaat alleen over de service zelf.**

`config.yaml` (in de installatiemap) regelt de infrastructuur: luisteradres, poort, datapaden — wijzigingen vereisen een herstart. Met de sectie `admin:` kun je gebruikersnaam en wachtwoord van de eerste beheerder vooraf instellen; dat geldt alleen zolang de database nog geen beheerder heeft, en het installatiescript met één klik verwijdert het wachtwoord in platte tekst zodra Diana is gestart. Bot- en modelconfiguratie staat in de database en werkt direct wanneer je die in de console wijzigt. De secties `bot:` / `llm:` in `config.yaml` worden **één keer** overgenomen, bij de eerste start met een lege database, en daarna genegeerd (het opstartlog vermeldt dat expliciet) — ze zijn bedoeld voor onbemande deployments.

Alle velden staan beschreven in [`config.example.yaml`](./config.example.yaml).

## Beveiliging

- Inloggen is vanaf de eerste start verplicht, met IP-gebaseerde blokkering tegen brute force.
- Opgeslagen API-sleutels en tokens worden nooit meer als platte tekst getoond.
- Stel achter een reverse proxy altijd `trusted_proxies` in `config.yaml` (of `DIANA_TRUSTED_PROXIES`) in.
- Met een eigenaarsaccount kun je snel inloggen via een eenmalige 6-cijferige code in een DM naar de bot, inclusief directe melding van IP en apparaat.

## Bijdragen

Backend in Go (Gin + SQLite), frontend in Vue 3 + TypeScript (`frontend/`).

```sh
make dev        # backend :18080 + Vite :5173
go test ./...   # backendtests
```

PR's zijn van harte welkom! Zie [CONTRIBUTING.md](./CONTRIBUTING.md) en [AGENTS.md](./AGENTS.md).

## Documentatie

| | |
| --- | --- |
| [Installatie](https://suink.github.io/Diana/en/deploy.html) | Alle installatiemethoden, serveropzet, eerste login |
| [Configuratie](https://suink.github.io/Diana/en/configuration.html) | Kanalen, modelrouting, groepsbeleid, beveiligingsgrenzen |
| [Implementatie](https://suink.github.io/Diana/en/implementation.html) | Architectuur, beslissingspijplijn, geheugenlagen |
| [Pijplijn](https://suink.github.io/Diana/en/pipeline.html) | Stroomschema's voor triggers, agentruns, browser en controle |
| [Beheer](https://suink.github.io/Diana/en/operations.html) | Updates, back-ups, probleemoplossing |
| [Live demo](https://suink.github.io/Diana/demo/) | De echte console met voorbeelddata |

## Licentie

`Limited Redistribution License (SuInk)` — zie [LICENSE](./LICENSE).
