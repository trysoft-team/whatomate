# Setup wizard: restore from a backup

## Goal

A new install can be set up straight from a backup. On the setup wizard's
Welcome screen the operator chooses **Start fresh** (today's flow) or
**Restore from a backup**. The restore path keeps the Checks and Database
steps, replaces the Admin step with a Restore step, restores the backup into
the chosen empty database, writes the config file and hands over to the app.
The operator then signs in with an account from the backup.

This replaces today's workaround for moving servers or databases: finish
setup with a throwaway admin, sign in, upload the backup in Settings →
Backup & Restore, restore, sign in again.

## Constraints

- Builds on the setup wizard (`internal/setup`, `frontend/src/views/setup`)
  and the backup engine (`internal/backup`). No new Go modules;
  `CGO_ENABLED=0`, Go 1.25, gorm v1.25.7.
- Works for every driver the wizard offers: SQLite, Postgres, MySQL, and
  across them (a SQLite backup into Postgres and so on).
- The wizard's existing protections stay: `POST` bodies must be
  `application/json`; the config file is written last and exclusively; a
  database that already has users is refused; endpoints exist only while
  setup runs (no config file yet).
- English strings only in `frontend/src/i18n/locales/en.json`.
- Never print secrets (DSNs, passwords, keys).

## Decisions

| Topic | Decision |
|---|---|
| Where the choice is made | Welcome screen: two cards, **Start fresh** and **Restore from a backup**. |
| Steps | Fresh: Welcome → Checks → Database → Admin (unchanged). Restore: Welcome → Checks → Database → Restore. The step list on the left follows the choice. |
| Backup source | Both: upload from the browser (chunked) or pick a backup already on the server: the `.zip` files in the chosen data folder's `backups` folder, or an absolute path typed by the operator. |
| Encryption key | Optional field for the old server's `encryption_key`, checked against the backup's key fingerprint. Blank: a new key is generated and the screen warns that saved WhatsApp tokens, AI provider keys and SSO secrets must be re-entered. A non-blank key that does not match is refused. A backup made without a key needs none and shows no warning. |
| Target database | Must have no users, as in the fresh flow. A restore can never wipe an existing install from the wizard. |
| Safety backup | None: the target database has no users, so there is nothing to keep. |
| Files | Restored into the new install's default media and audio folders (`<data folder>/uploads`, `<data folder>/audio`, the defaults setup's config resolves to). |
| Uploaded file | Kept in `<data folder>/backups`, so it is listed later in Settings → Backup & Restore. |
| Upload limit | 10240 MB, the `[backup] max_upload_mb` default. |
| Admin | No admin is created. The operator signs in with an account from the backup. |
| Approach | The setup server runs the restore itself with `internal/backup` (inspect, `Restore`), then writes the config last. |

## User flow

1. **Welcome.** Two cards. Choosing one sets the mode and goes to Checks.
2. **Checks** and **Database**: unchanged. The Database step's "has users"
   refusal applies to both modes.
3. **Restore step.**
   - Two tabs: **Upload a backup** and **On this server**.
   - Upload: pick a `.zip`; a progress bar shows the chunks. Cancel removes
     the partial file.
   - On this server: a list of backups in `<data folder>/backups` (name,
     date, size) and a field for a full path to a `.zip` elsewhere.
   - Once a backup is chosen the screen shows its summary: date, whatomate
     version, source database → target database, rows and tables, files
     (count and size) or "database only".
   - **Old encryption key** (optional, password-style input). As the
     operator types, the wizard checks it: "Matches the backup" or "Does not
     match this backup". Blank shows the re-enter-secrets warning.
   - **Restore and finish** is enabled once a backup is chosen and the key is
     blank or matching.
4. **Progress.** The finishing screen lists: preparing the database, loading
   data, linking records, putting files back, saving settings, starting
   whatomate. Then the same hand-off as a fresh finish: the browser goes to
   the app's sign-in page (on the new port if it changed).
5. **Sign in** with an account from the backup.

## Server side

All new endpoints live in `internal/setup` beside the existing ones and
answer only while setup is running.

| Method and path | Body / query | Answer |
|---|---|---|
| `POST /api/setup/backup-uploads` | JSON `{config_path, name, size}` | `{id, received, chunk_max}`; refuses sizes over the limit or a full disk |
| `PUT /api/setup/backup-uploads/{id}?offset=N` | raw bytes, `Content-Type: application/octet-stream` | `{received}`; 409 on a wrong offset |
| `POST /api/setup/backup-uploads/{id}/finish` | JSON `{}` | `{name, size}` after the file is renamed into `<data folder>/backups` and its manifest reads |
| `DELETE /api/setup/backup-uploads/{id}` | none | removes the partial file |
| `GET /api/setup/backups?config_path=` | none | `{dir, backups: [{name, size, manifest?, error?}]}` |
| `POST /api/setup/backup/inspect` | JSON `{config_path, database, backup: {name} or {path}, encryption_key}` | `{manifest, target_driver, key_matches, key_given}` |
| `POST /api/setup/restore` | JSON `{config_path, port, database, backup, encryption_key}` | starts the job; `{started: true}` |
| `GET /api/setup/restore` | none | `{running, phase, table, done, total, finished, failed, error, files_failed}` |

- **Upload** reuses the app's upload rules: one in-flight writer per upload
  id, offsets checked, `.part` file created exclusively with mode 0600 inside
  `<data folder>/backups`, renamed on finish with `backup.UploadName`. Chunks
  are at most 8 MB. The raw-chunk `PUT` is not a CORS simple request, so
  another website cannot send it from the operator's browser without a
  preflight, which the wizard never grants. `POST` stays JSON-only.
- **A backup reference** is either `{name}`, a file directly in
  `<data folder>/backups` checked with `backup.ValidName`, or `{path}`, an
  absolute path ending in `.zip`. Nothing else is accepted.
- **Inspect** reads the manifest with `backup.Inspect` against the target
  database's driver and this version's tables (no data read; the target may
  be empty). `key_matches` compares `backup.KeyFingerprint(encryption_key)`
  with the manifest; `key_given` says whether a key was entered. A backup
  with no key fingerprint (made without an encryption key) has no secrets to
  protect: any key, or none, matches and no warning is shown.
- **Restore** validates exactly as the fresh finish does (known config
  location, free port, valid database settings, no existing config file, no
  users in the database), then also: the backup reference resolves, and a
  non-blank key matches. It holds the same lock as the fresh finish (one
  finish at a time) and refuses while a restore job runs.
- **Restore job**, in the background:
  1. Open the database (create the SQLite folder as today).
  2. `backup.Restore` into it: verify the archive, prepare the tables, load
     every table in one transaction, then put the files back into the
     resolved `uploads` and `audio` folders. The data folder's `backups`
     folder and the config path are protected paths. No default admin is
     created.
  3. Write the config last: same values as a fresh finish, with
     `encryption_key` = the operator's key when it matched, else a new one.
  4. Mark setup finished and hand over to the app as a fresh finish does.
- **Progress** comes from the restore's progress callback; the wizard adds
  "saving settings" and "starting whatomate".
- The setup server's 30-second timeouts stay for everything except chunk
  `PUT`s (5 minutes). The job runs outside any request.

## Failures

- **Data fails to load:** the transaction rolls back, no config is written,
  and the progress answer has `failed` and the error. The wizard returns to
  the Restore step with the error shown; the operator can retry, pick
  another backup, or go back and choose another database.
- **Files fail after the data loaded:** the database is usable, so the
  config is still written and the app starts. The progress answer has
  `files_failed` and names the folder; the finishing screen says the data
  was restored but some files were not, then continues to sign-in.
- **Key does not match:** refused by inspect and by restore before anything
  starts.
- **Database has users / config already exists / port taken:** the same
  errors as the fresh finish, before anything starts.
- **Server stopped mid-restore, or the config fails to write:** no config
  exists, so the wizard runs again (or stays up). The data may already be
  committed, so the database can hold the backup's users. Before the job
  starts, the wizard writes `<data folder>/.setup-restore-pending` (mode
  0600) holding only the database's identity (driver, host, port and name,
  or the SQLite path; no password). A restore into a database with users is
  accepted when that marker names the same database: the restore empties the
  tables first. The marker is removed once a config file is written (by a
  restore or a fresh finish); a failed load leaves it. The fresh finish never
  accepts a database with users because of the marker.

## Security

Restore makes the setup port worth attacking. Without a key, anyone who could
reach it (it listens on every interface) could list the server's backups, or
name any `.zip` path, and restore it into a database they control: every row,
including users, tokens and encrypted secrets, would be copied to them, and
the config would be written pointing at their database. So the whole wizard
needs a **one-time setup token**:

- `Run` makes a random token (24 bytes from `crypto/rand`, base64url) when it
  starts and keeps it only in memory; a new run gets a new token.
- The token rides in the URL **fragment** of every wizard URL it logs and of
  the one it opens in the browser (`http://localhost:4747/#token=…`).
  Browsers never send the fragment, so the token stays out of request logs,
  proxies and `Referer` headers. These startup log lines are the only place
  it is printed.
- The page moves the token from the address bar into `sessionStorage`
  (`history.replaceState`) before the router starts, and sends it as
  `X-Setup-Token` on every `/api/setup/*` call, chunk `PUT`s included.
- The server compares the header in constant time (`crypto/subtle`) on every
  `/api/setup/*` route and answers 401 without it. The page and its assets
  stay public. A custom header also makes every setup call a CORS
  non-simple request, which another website cannot send from the operator's
  browser.
- `GET /api/setup/status` still answers without the token, with only
  `{needed: true, version, token_required: true}` (no config locations or
  paths), so the page can ask for the link; after the hand-off it answers
  `{needed: false, url}` as before, which the page's hand-off polling needs.
- Without a valid token (or after a 401) the page explains that the full link
  from the server's log is needed, and takes the link or the bare key.
- The old setup port's redirect after a port change needs no token: it only
  points at the app.

Anyone who has the link can still finish setup, so the docs say to keep it
private and run the first setup on a private network. A typed path lets the
operator read any `.zip` the process can read, but only as a backup: it must
pass `backup.Inspect`, and nothing from it is returned except the manifest
summary. The follow-up `whatomate-unk` (allowlist the Host header against DNS
rebinding) still applies.

## Frontend

- `SetupView.vue`: a `mode` (`fresh` | `restore`) chosen on Welcome; the
  step list comes from a helper in `lib/setup.ts` (`stepsFor(mode)`).
- New `SetupRestore.vue` for the Restore step (tabs, list, upload progress,
  summary, key field). Reuses `uploadInChunks` and `formatBytes` from
  `lib/backup.ts`.
- `setupService` gains the new calls.
- The finishing screen shows the restore phases while the job runs, then
  the existing "starting whatomate" hand-off.

## Testing

- Go (`internal/setup`):
  - upload: start, chunks, wrong offset, finish, cancel, size limit;
  - backup list for a data folder;
  - inspect with no key, matching key, wrong key; bad references (`..`,
    relative path, not `.zip`);
  - restore success on SQLite: config written with the old key, users from
    the backup present, files in `uploads`;
  - refusals: database with users, existing config, wrong key;
  - a failed load leaves no config and the database stays reusable.
- One cross-database test: a SQLite backup restored through the wizard into
  `whatomate_test_backup` on Postgres (env-gated like the backup tests).
- Vitest: `stepsFor(mode)` and the key-match state.
- Manual walkthrough on throwaway instances (spare ports, never 8080 or the
  dev database): restore a backup made by one instance into a fresh one via
  the wizard, sign in with the backup's admin; screenshots in both themes.
