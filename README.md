<a href="https://zerodha.tech"><img src="https://zerodha.tech/static/images/github-badge.svg" align="right" alt="Zerodha Tech Badge" /></a>

# Whatomate

Modern, open-source WhatsApp Business Platform. Single binary app.

![Dashboard](docs/public/images/dashboard-light.png#gh-light-mode-only)
![Dashboard](docs/public/images/dashboard-dark.png#gh-dark-mode-only)

## Features

- **Multi-tenant Architecture**
  Support multiple organizations with isolated data and configurations.

- **Granular Roles & Permissions**
  Customizable roles with fine-grained permissions. Create custom roles, assign specific permissions per resource (users, contacts, templates, etc.), and control access at the action level (read, create, update, delete). Super admins can manage multiple organizations.

- **WhatsApp Cloud API Integration**
  Connect with Meta's WhatsApp Business API for messaging.

- **Real-time Chat**
  Live messaging with WebSocket support for instant communication.

- **Template Management**
  Create and manage message templates approved by Meta.

- **Bulk Campaigns**
  Send campaigns to multiple contacts with retry support for failed messages.

- **Chatbot Automation**
  Keyword-based auto-replies, conversation flows with branching logic, and AI-powered responses (OpenAI, Anthropic, Google).

- **Canned Responses**
  Pre-defined quick replies with slash commands (`/shortcut`) and dynamic placeholders.

- **Voice Calling & IVR**
  Incoming and outgoing WhatsApp calls with IVR menus, DTMF routing, call transfers to agent teams, hold music, and call recording. See [calling docs](https://trysoft-team.github.io/whatomate/features/calling/).

- **Analytics Dashboard**
  Track messages, engagement, and campaign performance.

- **Use the Database You Already Have**
  Run on PostgreSQL, MySQL 8.0.19+ / MariaDB 10.6+, or SQLite (one file, no database server). Pick one with `[database] driver` in `config.toml`. See [database setup](https://trysoft-team.github.io/whatomate/getting-started/configuration/#database-setup).

<details>
<summary>View more screenshots</summary>

![Dashboard](docs/public/images/dashboard-light.png#gh-light-mode-only)
![Dashboard](docs/public/images/dashboard-dark.png#gh-dark-mode-only)
![Chatbot](docs/public/images/chatbot-light.png#gh-light-mode-only)
![Chatbot](docs/public/images/chatbot-dark.png#gh-dark-mode-only)
![Agent Analytics](docs/public/images/agent-analytics-light.png#gh-light-mode-only)
![Agent Analytics](docs/public/images/agent-analytics-dark.png#gh-dark-mode-only)
![Conversation Flow Builder](docs/public/images/conversation-flow-light.png#gh-light-mode-only)
![Conversation Flow Builder](docs/public/images/conversation-flow-dark.png#gh-dark-mode-only)
![Templates](docs/public/images/11-templates.png)
![Campaigns](docs/public/images/13-campaigns.png)

</details>

## Installation

### Docker

Prebuilt images are not published yet. Build and run from source with Docker Compose:

```bash
git clone https://github.com/trysoft-team/whatomate.git
cd whatomate

# Copy and edit config and env (PostgreSQL credentials, timezone)
cp config.example.toml config.toml
cp .env.example .env

# Build the image and start the app, PostgreSQL and Redis
docker compose up -d --build
```

Go to `http://localhost:8080` and login with `admin@admin.com` / `admin`

__________________

### Binary

Download the [latest release](https://github.com/trysoft-team/whatomate/releases) for Linux (`amd64`, `arm64`, `armv7`), macOS (one universal binary for Apple Silicon and Intel), Windows or FreeBSD, and extract it.

```bash
# Copy and edit config: choose [database] driver = "sqlite", "postgres" or "mysql"
cp config.example.toml config.toml

# Run with migrations
./whatomate server -migrate
```

Redis is required with every database. Go to `http://localhost:8080` and login with `admin@admin.com` / `admin`

__________________

### Homebrew (macOS and Linux)

```bash
brew install trysoft-team/tap/whatomate
brew upgrade whatomate   # after a new release
```

The install output shows where the bundled `config.example.toml` is and how to start the server.

__________________

### Build from Source

```bash
git clone https://github.com/trysoft-team/whatomate.git
cd whatomate

# Production build (single binary with embedded frontend)
make build-prod
./whatomate server -migrate
```

See [configuration docs](https://trysoft-team.github.io/whatomate/getting-started/configuration/) for detailed setup options.

## CLI Usage

```bash
./whatomate server              # API + 1 worker (default)
./whatomate server -workers=0   # API only
./whatomate worker -workers=4   # Workers only (for scaling)
./whatomate version             # Show version
```

## Developers

The backend is written in Go ([Fastglue](https://github.com/zerodha/fastglue)) and the frontend is Vue.js 3 with shadcn-vue.
- If you are interested in contributing, please read [CONTRIBUTING.md](./CONTRIBUTING.md) first.

Preferred local loop (host Go + Vite; Postgres/Redis in Docker):

```bash
# One-time: copy config and point DB/Redis at the host
cp config.toml config.local.toml
# In config.local.toml: database.host=localhost, database.port=5433, redis.host=localhost

make docker-infra          # Postgres :5433 + Redis :6379; stops the app container
make run-migrate           # API on :8080 (uses config.local.toml)
cd frontend && npm run dev # UI on :3000 (proxies /api → :8080)
# or: make dev
```

Login: `admin@admin.com` / `admin` at http://localhost:3000

### Releases

`main` is the development branch; pull requests and pushes to it run the full test suite on PostgreSQL, SQLite, MySQL and MariaDB. Every push to `master` runs the ~1,100 core tests on PostgreSQL (`make test-release`), tags the next version (`v0.2.0`, then `v0.2.1`, ...) and publishes the binaries to [GitHub Releases](https://github.com/trysoft-team/whatomate/releases) and the cask to [trysoft-team/homebrew-tap](https://github.com/trysoft-team/homebrew-tap). To release:

```bash
git checkout master && git merge --ff-only main && git push origin master
```

Pushes that only change docs or Markdown don't release. For a minor or major bump, run the **Release** workflow from the Actions tab on `master` and pick the part to bump, or push a `vX.Y.Z` tag yourself. `make release-snapshot` builds the same archives locally into `dist/` without publishing.

## License

See [LICENSE](LICENSE) for details.
