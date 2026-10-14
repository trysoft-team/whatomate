<a href="https://zerodha.tech"><img src="https://zerodha.tech/static/images/github-badge.svg" align="right" alt="Zerodha Tech Badge" /></a>

# Whatomate

Modern, open-source WhatsApp Business Platform. One binary, a setup wizard in the browser, and the database you already run.

![Dashboard](docs/public/images/dashboard-light.png#gh-light-mode-only)
![Dashboard](docs/public/images/dashboard-dark.png#gh-dark-mode-only)

**Documentation:** [trysoft-team.github.io/whatomate](https://trysoft-team.github.io/whatomate/)

## Features

### Conversations
- **Shared team inbox** — real-time two-way WhatsApp chat over WebSockets, with assignment, tags, notes, contact details and the 24-hour service window shown on every conversation.
- **AI assist for agents** — suggest a reply, rewrite or translate a draft, quick-reply chips, translate incoming messages, transcribe voice notes and pick a template when the 24-hour window has closed (OpenAI, Anthropic, Google AI or OpenRouter).
- **Canned responses** — quick replies with `/shortcut` commands, placeholders, interactive buttons and WhatsApp Flows.
- **Media** — send and view images, video, audio, documents, stickers and locations, with an in-app viewer.
- **Teams** — route chats and calls to agent teams with per-agent rotation.

### Automation
- **Chatbot** — keyword rules, AI replies with your own context, greeting and fallback messages, and a default flow per WhatsApp account.
- **Visual flow builder** — branching conversation flows with buttons, lists, media, conditions, HTTP calls and variables, plus a playground to try a flow before going live.
- **WhatsApp Flows** — build, preview, publish and send Meta's native interactive forms.
- **Custom actions & outbound webhooks** — per-contact buttons that call your systems, and HMAC-signed, retried event deliveries.

### Messaging at scale
- **Templates** — create, edit and resubmit Meta templates, including media headers, dynamic URL buttons, OTP and voice-call buttons, in every Meta-supported language.
- **Campaigns** — bulk sends to contact lists with recipient validation, progress tracking and retries.
- **Product catalogs** — sync catalogs and products from Meta Commerce Manager.

### Voice calling
- **WhatsApp calls** — incoming and outgoing calls in the browser, with hold music, transfers to agents and teams, and call recording.
- **IVR flow builder** — menus, DTMF input, HTTP callbacks, transfers and text-to-speech greetings.
- **AI calls** — a Gemini Live voice agent can answer callers inside a call flow, call your functions and hand over to a person.

### Insight and control
- **Dashboard & analytics** — customizable widgets, agent analytics and Meta Insights.
- **Multi-tenant** — many organizations on one server, with isolated data and settings.
- **Roles & permissions** — custom roles with per-resource, per-action permissions.
- **Sign-in options** — password, passkeys, and SSO with Google, Microsoft, GitHub, Facebook or any OIDC provider.
- **Audit logs** — who changed what, with field-level differences.
- **API keys** — full REST API for your own integrations.
- **Languages** — English, Arabic, Spanish, Portuguese, Hindi and Tamil.

### Runs anywhere
- **Your database** — PostgreSQL, MySQL 8.0.19+ / MariaDB 10.6+, or SQLite (one file, no database server).
- **No Redis needed** — a built-in store handles caching, queues and pub/sub; add Redis when you run several servers.
- **Setup wizard** — the first run opens a browser wizard that checks the system, sets up the database and the first admin, or restores a backup.
- **Backup & Restore** — back up the whole server (database and files) to one file and restore it, even onto a different database type.
- **Single binary** — the web app is embedded; releases for Linux, macOS, Windows and FreeBSD, plus Homebrew.

<details>
<summary>More screenshots</summary>

![Chatbot](docs/public/images/chatbot-light.png#gh-light-mode-only)
![Chatbot](docs/public/images/chatbot-dark.png#gh-dark-mode-only)
![Agent Analytics](docs/public/images/agent-analytics-light.png#gh-light-mode-only)
![Agent Analytics](docs/public/images/agent-analytics-dark.png#gh-dark-mode-only)
![Conversation Flow Builder](docs/public/images/conversation-flow-light.png#gh-light-mode-only)
![Conversation Flow Builder](docs/public/images/conversation-flow-dark.png#gh-dark-mode-only)
![Templates](docs/public/images/11-templates.png)
![Campaigns](docs/public/images/13-campaigns.png)

</details>

## Getting started

### 1. Install

**Homebrew (macOS and Linux)**

```bash
brew install trysoft-team/tap/whatomate
```

**Binary** — download the [latest release](https://github.com/trysoft-team/whatomate/releases/latest) for Linux (`amd64`, `arm64`, `armv7`), macOS (universal), Windows (`amd64`, `arm64`) or FreeBSD, and extract it.

**From source** — needs Go 1.25+ and Node.js 20.19+:

```bash
git clone https://github.com/trysoft-team/whatomate.git
cd whatomate
make build-prod   # single binary with the web app embedded
```

### 2. Run

```bash
whatomate          # or ./whatomate
```

With no config file, whatomate opens the **setup wizard** at `http://localhost:4747`. The server log prints the wizard link, which carries a one-time setup key; open that link.

1. **Start fresh** — pick a database (SQLite needs nothing installed; or enter a PostgreSQL / MySQL server), then create your organization and admin account.
2. **Or restore from a backup** — upload a backup file or pick one on the server, and sign in with an account from the backup.

The wizard writes the config file and starts whatomate on the same port. Then connect your WhatsApp Business Account under **Settings → Accounts** (see [WhatsApp configuration](https://trysoft-team.github.io/whatomate/getting-started/quickstart/#whatsapp-configuration)).

> [!TIP]
> Voice features use `ffmpeg` and `opus-tools`; text-to-speech also uses Piper and `espeak-ng`. Homebrew installs `ffmpeg` and `opus-tools` for you.

### Docker

Prebuilt images are not published yet. Build and run from source with Docker Compose (app + PostgreSQL):

```bash
git clone https://github.com/trysoft-team/whatomate.git
cd whatomate
cp config.example.toml config.toml
cp .env.example .env          # PostgreSQL credentials and timezone
docker compose up -d --build
```

Open `http://localhost:4747` and sign in with `admin@admin.com` / `admin`, then change the password.

## Command line

```bash
whatomate                          # same as "server"; runs the setup wizard if there is no config
whatomate server                   # API + 1 background worker
whatomate server -workers=0        # API only
whatomate worker -workers=4        # background workers only (for scaling out)
whatomate backup                   # back up the database and files into the backup folder
whatomate restore <backup.zip>     # restore a backup into the configured database
whatomate config path              # show which config file is used
whatomate version
```

The config file is found with `-config`, then `$WHATOMATE_CONFIG`, then the per-OS locations listed in `whatomate help` (for example `~/.config/whatomate/config.toml`). See the [configuration reference](https://trysoft-team.github.io/whatomate/getting-started/configuration/).

## Development

The backend is Go ([Fastglue](https://github.com/zerodha/fastglue)) and the frontend is Vue 3 with shadcn-vue. Please read [CONTRIBUTING.md](./CONTRIBUTING.md) first.

```bash
# One-time: a local config pointing at the database you develop against
cp config.example.toml config.local.toml
# e.g. [database] driver = "sqlite", or PostgreSQL from `make docker-infra` on localhost:5433

make docker-infra          # optional: PostgreSQL on :5433
make run-migrate           # API on :4747 (uses config.local.toml)
cd frontend && npm run dev # UI on :3000, proxies /api to :4747
```

Tests: `make test` (PostgreSQL), `make test-sqlite`, `make test-mysql`; frontend unit tests `cd frontend && npm run test:unit`, end-to-end tests `npm run test` against a running server.

### Releases

`main` is the development branch: pushes run the full test suite on PostgreSQL, SQLite, MySQL and MariaDB. Every push to `master` runs the core tests (`make test-release`), tags the next patch version and publishes the binaries to [GitHub Releases](https://github.com/trysoft-team/whatomate/releases) and the cask to [trysoft-team/homebrew-tap](https://github.com/trysoft-team/homebrew-tap). For a minor or major version, push a `vX.Y.Z` tag with the `master` commit, or run the **Release** workflow and pick the part to bump. Pushes that only change docs or Markdown don't release. `make release-snapshot` builds the archives locally into `dist/`.

## License

See [LICENSE](LICENSE) for details.
