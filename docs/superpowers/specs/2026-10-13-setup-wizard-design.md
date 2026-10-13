# First-run setup wizard

## Goal

A user installs whatomate (Homebrew, apt, a Windows `.exe`, a plain binary)
and starts it. If there is no config file, the binary starts a setup wizard
in the browser instead of exiting with an error. The wizard checks the
system, asks which database to use and its details, creates the first admin,
writes the config file and then carries on as the normal app in the same
process.

Port 4747 becomes the default port for both the wizard and the app.

## Constraints

- Pure Go, `CGO_ENABLED=0`, Go 1.25. No new external services.
- The wizard UI is part of the existing Vue frontend (same components and
  styling) and is embedded in the binary like the rest of the frontend.
- Builds on config discovery (`internal/config/locate.go`): the wizard only
  runs when `config.Locate` finds nothing.
- Existing configs keep working. Configs that set `[server] port` keep their
  port.
- SQL Server is not offered: `database.Open` does not support it yet.

## Decisions

| Topic | Decision |
|---|---|
| Default port | 4747 for the wizard and the app. Changeable in the wizard and later in Settings → General. |
| Protection | None. The first person to open the wizard completes setup. The wizard can never run again once a config file exists. |
| Browser | Always try to open the browser, and always print the URLs. |
| Config location | System folder if writable, otherwise the user folder. Shown in the wizard with an option to change it. |
| System checks | OS/arch/version, folders writable, free disk, free memory, port free, internet (Meta Graph API), clock, audio tools. |
| Database | SQLite preselected. Postgres/MySQL have Test connection and offer to create a missing database, then migrate. |
| Admin step | Organization name, full name, email, password (confirm + strength hint). |
| Secrets | JWT secret and encryption key generated automatically, never shown. |
| Finish | Same process switches into the normal app. No restart. |
| Extra steps | None. Public URL, WhatsApp, storage and Redis stay in the normal settings / config. |

## Startup

```
whatomate            (no arguments)  ─┐
whatomate server [-config f]         ─┴─> config.Locate
                                            │
                          found ────────────┼──────────── not found
                            │                                │
                      normal startup                 setup mode (port 4747)
                                                             │
                                                    wizard completes
                                                             │
                                          config written, DB migrated, admin created
                                                             │
                                          normal startup with the new config,
                                          reusing the setup listener
```

- **No arguments runs `server`.** Today it prints usage and exits 1, so
  double-clicking the Windows `.exe` only flashes a window. `help`, `-h` and
  `--help` still print usage.
- **Target path.** If `-config` or `WHATOMATE_CONFIG` names a file that does
  not exist, setup writes to exactly that path and the location picker is
  hidden. Otherwise the picker offers the user and system config paths from
  `locate.go`.
- **`whatomate worker`** never runs setup. With no config it exits with
  "no config found; run `whatomate server` first to finish setup".
- **`whatomate config path`** keeps reporting "not found" with the searched
  paths.
- **Listening.** Setup binds `0.0.0.0:4747`. If 4747 is busy it binds a free
  port (`:0`) and uses that. It logs `http://localhost:<port>` plus one URL
  per non-loopback IPv4 address, and calls the platform opener (`open`,
  `xdg-open`, `rundll32 url.dll,FileProtocolHandler`). Opener failures are
  logged at debug level only.
- `config.Locate` returns a distinct `ErrNotFound` (wrapped, with the
  searched paths) so `runServer` can tell "nothing found" apart from "found
  but unreadable". Unreadable or invalid configs still exit with an error;
  setup never overwrites an existing file.

## Default port

- `config.go` default, `config.example.toml`, docs (quickstart,
  configuration, reverse-proxy examples), `docker-compose.yml`, Dockerfile
  `EXPOSE`, `.goreleaser.yml` caveats and the frontend dev proxy / Playwright
  `BASE_URL` change from 8080 to 4747.
- Release note: a config without `[server] port` moves from 8080 to 4747.

## Wizard screens

Route `/setup`, a full-page layout outside the app shell, with a step
indicator. Strings go through i18n like the rest of the frontend.

1. **Welcome.** Product name, version, one paragraph on what setup does,
   Start button.
2. **System check.**
   - A list of checks with pass / warning / fail, detail text and a Re-run
     button.
   - **Port** field (default: the port setup is listening on). Changing it
     re-runs the port check.
   - **Config location**: the chosen path, with a picker listing the
     candidates and whether each is writable.
   - Next is disabled while any check fails.
3. **Database.**
   - Choice: SQLite (preselected), PostgreSQL, MySQL.
   - SQLite: file path, default `<data dir>/whatomate.db`.
   - PostgreSQL: host, port (5432), user, password, database, SSL mode.
   - MySQL: host, port (3306), user, password, database.
   - Test connection shows success or the driver error. If the server is
     reachable but the database does not exist, a "Create database" button
     appears.
   - Next requires a successful test (SQLite: the folder must be writable).
4. **Admin.**
   - Organization name, full name, email, password, confirm password.
   - Password: minimum 8 characters, strength hint, must match.
   - **Finish** shows a progress screen in two phases: "Setting up the
     database, admin account and config file" (the `complete` request), then
     "Starting whatomate" (waiting for the app). Errors show the server's
     message with a Back button; nothing is half-written (see "Failure
     handling").

### System checks

| Check | Fail | Warning |
|---|---|---|
| OS / architecture / version | — | — (info only) |
| Config folder and data folder writable | not writable | — |
| Free disk on data folder | — | below 1 GB |
| Memory (total RAM) | — | below 512 MB |
| Port free | taken by another process | — |
| Internet: HTTPS to `graph.facebook.com` | — | unreachable within 5 s |
| Clock: `Date` header from the internet check | — | off by more than 2 minutes |
| Audio tools (`ffmpeg`, `opusenc` on PATH) | — | missing (calling hold music / TTS conversion won't work) |

The port check passes when the chosen port is the one setup is listening on.
Disk and memory use pure-Go system calls per OS (`golang.org/x/sys`), behind
one small interface so tests can fake them.

## Setup API

Only registered in setup mode, under the same fastglue router shape as the
app, plus the embedded frontend.

| Endpoint | Purpose |
|---|---|
| `GET /api/setup/status` | before completion `{needed:true, version, listen_port, config_path, config_path_fixed, candidates[{path, data_dir, writable}]}`; after completion, while setup is handing off `{needed:false, ready:false, url}`; from the app or the old-port responder `{needed:false, ready:true[, url]}` |
| `GET /api/setup/checks?port=&config_path=` | list of `{id, status: pass\|warn\|fail, detail}` |
| `POST /api/setup/database/test` | `{driver, path \| host, port, user, password, name, ssl_mode}` → `{ok, database_exists, error}` |
| `POST /api/setup/database/create` | same body; `CREATE DATABASE` (MySQL: `utf8mb4`) → `{ok, error}` |
| `POST /api/setup/complete` | `{config_path, port, database{…}, organization, admin{full_name, email, password}}` → `{url}` |

- In normal mode `GET /api/setup/status` returns `{needed:false}` and the
  other setup endpoints are not registered (404).
- The frontend calls `status` once on load. If `needed`, every route
  redirects to `/setup`; otherwise `/setup` redirects to `/login`.
- `complete` is serialised with a mutex; a second call after success returns
  409.
- Request bodies are never logged (they contain passwords).

## Completing setup

`POST /api/setup/complete` runs, in order:

1. Validate the input again on the server.
2. Open the database with the given settings. It must be empty (no users),
   unless an earlier attempt of this same setup run seeded it: same database
   target and the admin user ID that attempt created. Otherwise stop with
   "database already has users; choose an empty database".
3. Run `RunMigrationWithProgress` with a `DefaultAdminConfig` built from the
   wizard (new field `OrganizationName`), so the existing code creates the
   organization with the given name and the super-admin user.
4. Apply the wizard's admin details to that user (password hash, full name)
   and organization (name). This makes a retry after a later failure use the
   values from the retry.
5. Write the config file:
   - `MkdirAll` the folder with 0750, write to a temp file in the same
     folder with 0600, `fsync`, rename.
   - Contents, rendered from a template with TOML-escaped values:
     - `[app]` `environment`, generated `encryption_key` (32 random bytes,
       base64);
     - `[server]` `host = "0.0.0.0"`, `port`, `allowed_origins`;
     - `[database]` the chosen settings (SQLite path stays relative to the
       data folder when it is inside it);
     - `[jwt]` generated `secret` (48 random bytes, base64);
     - no `[default_admin]` section and no `[redis]` host.
   - The written file must load with `config.Load` and produce the same
     values (checked in tests).
6. Return `{url}` and hand off to normal startup (below).

### Environment and origin

- The origin is taken from the request's `Origin` header when it is a plain
  `http(s)://host[:port]`, else from `X-Forwarded-Proto` / `X-Forwarded-Host`
  if present, else scheme and `Host`. Without proxy headers, the port is
  replaced by the chosen port when that differs.
- Opened over HTTPS: `environment = "production"`,
  `allowed_origins = <origin>`.
- Opened over HTTP: `environment = "development"` (production forces
  `Secure` cookies and login would fail on `http://<lan-ip>`), and
  `allowed_origins` = the origin plus `http://localhost:<port>` and
  `http://<lan-ipv4>:<port>` for each LAN address, so teammates on another
  address get live updates. The finish screen says so and points to the
  docs for switching to production behind HTTPS.

### Default admin fix

`CreateDefaultAdmin` skips only when a user with the configured email
exists. After the wizard creates a different admin, a later
`whatomate server -migrate` would add `admin@admin.com` as a second super
admin with the default password. Change: skip when **any** user exists,
matching its doc comment. Covered by a test.

## Hand-off to the app

- Setup creates its own `net.Listener` and serves fasthttp on it. On
  completion it stops the setup fasthttp server without closing the listener
  (a shared listener whose views can be closed without closing the socket)
  and returns the listener to `runServer` (nil when the port changed).
- `runServer` then runs the config search again, which now finds the new
  file with the right data folder, and continues the existing startup. The
  final
  `server.ListenAndServe(addr)` becomes `server.Serve(ln)` when a listener
  was handed over. Connections that arrive during startup wait in the
  kernel backlog instead of being refused.
- **Same port.** The browser polls `GET /api/setup/status` until it returns
  `{needed:false}` (served by the normal app), then goes to `/login`.
- **Different port.** Setup closes its listener, normal startup binds the
  new port, and a tiny responder stays on the old port for 2 minutes,
  answering `/api/setup/status` with `{needed:false, ready:true, url}` and
  redirecting other paths there. The browser polls
  `fetch(<url>/health, {mode: "no-cors"})` until it resolves, then navigates
  to `<url>/login`. If the new port can't be bound, startup exits with an
  error as it does today (the config is already written; the message says
  which file to edit).

## Failure handling

- Any failure before the config file is written leaves no config behind, so
  the next start runs setup again.
- Migrations are idempotent. If migrations succeed but admin creation fails,
  retrying `complete` re-runs migrations and creates the admin.
- If the admin exists but writing the config fails (folder not writable),
  the error names the path. Retrying with another location succeeds because
  the wizard reuses a database its own earlier attempt seeded (same target
  and admin user ID, recorded as soon as that attempt created the admin, even
  if a later step failed), and step 4 re-applies the admin details. Any other
  database with users makes it fail with "database already has users; choose
  an empty database".
- Ctrl+C during setup exits cleanly without writing anything.

## Port in Settings → General

- New "Server" card in the General tab, visible to super admins only. Shows
  the port and the config file path.
- `GET /api/admin/server-settings` → `{port, config_port, config_path,
  writable}` (`port` is the running port, `config_port` the one in the
  file); `PUT /api/admin/server-settings` `{port}` (super admin only).
- The PUT edits only the `port = …` line inside `[server]` in the file the
  app loaded (adding it if missing), keeps everything else byte-for-byte,
  and writes via temp file + rename keeping the file mode. When the folder
  isn't writable but the file is, it rewrites the file in place.
- Response says a restart is needed; the card shows "Restart whatomate to
  apply". Not writable (e.g. root-owned `/etc`) → clear error with the path.
- Port 1–65535; a port other than the current one must be free.

## Security

- No setup code, by choice. Risk: on a machine reachable from the internet,
  anyone who opens the page first can create the admin and point the app at
  a database they control. The window closes as soon as a config file
  exists. Documented in the install docs with the advice to run first setup
  on a private network or behind a firewall.
- Setup endpoints exist only in setup mode.
- Config file 0600, folders 0750. Passwords are never logged or echoed back.
- Database "create" only issues `CREATE DATABASE <quoted name>` with the
  name validated against `^[A-Za-z0-9_]{1,63}$`.

## Testing

- Go:
  - `Locate` returns `ErrNotFound` when nothing exists.
  - each check with faked system info;
  - config writer round trip through `config.Load`;
  - setup endpoints end to end against a temp SQLite database and temp
    config folder, including failure and retry paths;
  - `CreateDefaultAdmin` skips when any user exists and uses
    `OrganizationName`;
  - listener hand-off (same port serves the app after completion);
  - port-line editing (missing section, missing key, comments kept).
- Postgres/MySQL `database/test` and `create` against the test servers
  (`whatomate_test` naming rules apply).
- Frontend: vitest for step validation and navigation, and the router guard.
- No Playwright against the dev database.

## Out of scope

- Setup code / token protection.
- Terminal (non-browser) setup.
- Configuring public URL, WhatsApp, storage or Redis in the wizard.
- Restarting the app from Settings.
- SQL Server.
- Packaging changes beyond the port number (apt package, Windows installer).
