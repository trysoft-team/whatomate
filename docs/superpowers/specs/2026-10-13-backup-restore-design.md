# Backup & Restore

## Goal

A super admin can back up the whole system from Settings → Backup & Restore
and restore it later. A backup holds every organization's data and,
optionally, the local files (chat media, flow media, IVR/TTS audio, local call
recordings). A backup made on one database type restores into another:
SQLite → Postgres, Postgres → MySQL and so on. That is how an install moves
to a different database: install whatomate on the new database, upload the
backup, restore.

The same engine is available from the command line as `whatomate backup` and
`whatomate restore <file>`.

## Constraints

- Pure Go, `CGO_ENABLED=0`, Go 1.25, gorm v1.25.7. No external dump tools.
- Works on every driver `database.Open` supports: Postgres, MySQL, SQLite.
- Super admins only, in the UI and on every endpoint.
- The running app keeps serving while a backup is made. A restore puts the
  app into maintenance mode.
- Secrets never leave the server in clear text beyond what the database
  already holds; `config.toml` is never part of a backup.

## Decisions

| Topic | Decision |
|---|---|
| Scope | Whole system: all tables in `database.GetMigrationModels()` plus the `role_permissions` join table. |
| Files | Local files included by default; "Include files" can be unticked. S3 recordings are not downloaded. |
| Storage | Backups are kept in a folder on the server and listed on the page. Made on demand; no schedules. |
| Upload | A backup made elsewhere can be uploaded into the list. |
| Restore target | Always the database whatomate is running on. |
| Restore mode | Replace everything. No merging. |
| Format | Zip with a manifest and one JSON-lines file per table, written through whatomate's models. |
| Safety | Every restore first takes an automatic `pre-restore` backup of the current state. |
| Confirmation | Typing `RESTORE`. |
| Compatibility | Older backups restore into newer whatomate. Backups from a newer whatomate are refused. |
| Encryption key | A fingerprint of `encryption_key` is stored; a mismatch is a loud warning, not a block. |
| CLI | `whatomate backup` and `whatomate restore <file>`. |

## Configuration

```toml
[backup]
dir = "backups"         # relative paths resolve like other paths: against the data folder
max_upload_mb = 10240   # largest backup file the page accepts for upload
```

`Config.ResolvePaths` joins `Backup.Dir` like `Storage.LocalPath`. An empty
`dir` means `backups`; a zero `max_upload_mb` means 10240. The setup wizard does not need to write it. For the
Homebrew system install this lands in `/opt/homebrew/var/whatomate/backups`.

## Archive format

File name: `whatomate-backup-<UTC yyyymmddThhmmssZ>.zip`, or
`whatomate-pre-restore-<…>.zip` for safety backups. Uploaded files keep a
sanitized version of their own name (letters, digits, `-`, `_`, `.`; must end
in `.zip`; a numeric suffix is added on clash).

```
manifest.json
db/<table>.jsonl          one JSON object per row, keys are column names
files/media/...           storage.local_path (chat media, flow media and local call recordings)
files/audio/...           calling.audio_dir
```

`manifest.json`:

```json
{
  "format": 1,
  "app_version": "v0.1.1-beta-0.17-153-g2201d1b",
  "kind": "manual",
  "created_at": "2026-10-13T18:15:30Z",
  "source_driver": "sqlite",
  "key_fingerprint": "3f9a1c0d7b2e",
  "include_files": true,
  "tables": [
    { "name": "organizations", "rows": 3, "sha256": "…",
      "columns": ["id", "name", "slug", "settings", "created_at", "…"] }
  ],
  "files": { "media": { "count": 412, "bytes": 88123456 }, "audio": { "…": 0 } }
}
```

- `format` is the archive layout version. A reader refuses a higher number.
- `kind` is `manual`, `pre-restore` or `cli`.
- `key_fingerprint` is the first 12 hex characters of
  `sha256("whatomate-backup-key:" + encryption_key)`; empty when no key is
  set. The key itself is never written.
- Each table lists its columns as they existed when the backup was made, and
  a SHA-256 of its `.jsonl` entry. File entries rely on the zip CRC-32.
- Not backed up: `kv_entries`, `scheduled_items`, `campaign_jobs` (embedded
  store state). Restore empties them.

### Row encoding

Model JSON tags hide secrets (`json:"-"`), so rows are not marshalled through
the models' JSON. Each table is read in primary-key order, in batches of 1000,
`Unscoped` (soft-deleted rows included), into the model struct so each
driver's own scanning and `Valuer`/`Scanner` types apply. Each row is then
encoded column by column from the gorm schema (`field.DBName` →
`field.ValueOf`):

| Go value | JSON |
|---|---|
| `nil` / NULL | `null` |
| string, numbers, bool | as is (ints that do not fit 2^53 as strings) |
| `time.Time` | RFC 3339 with nanoseconds, UTC |
| `uuid.UUID` | string |
| `JSONB`, `JSONBArray`, `StringArray` | embedded JSON value |
| `[]byte` | base64 string |
| decimals | string |

`role_permissions` has no model; it is read and written as plain
`custom_role_id` / `permission_id` string pairs.

## Backup

1. Take the job lock (one backup or restore at a time, process-wide).
2. Create `<dir>/.tmp-<random>.zip`.
3. For each table: stream rows into `db/<table>.jsonl`, hashing as it goes,
   reporting progress (table name, rows done, total from a `COUNT(*)`).
4. If files are included, walk each folder and add regular files (symlinks
   and anything outside the folder are skipped).
5. Write `manifest.json` last, close, `fsync`, rename to the final name.

Reads are not wrapped in one long transaction, so a backup taken while
messages arrive can be a few rows apart between tables. This is acceptable
for a manual backup and avoids holding locks on SQLite.

On failure or cancel the temp file is removed; nothing appears in the list.
Temp files left by a crash are removed at startup.

## Restore

### Inspect

`Inspect(file)` opens the zip and returns a summary without changing
anything:

- `format` supported, manifest readable, every `.jsonl` SHA-256 and every
  file CRC good.
- Compatibility: every table and column in the backup is known to the
  running version. An unknown table or column means the backup came from a
  newer whatomate → refused with "made by whatomate X; upgrade this server
  first". Columns the backup lacks get their defaults.
- Summary: date, kind, size, source driver → this driver, row counts,
  whether files are included, key fingerprint match / mismatch / no key.

### Run

1. Inspect again; refuse on any error. Require `confirm == "RESTORE"`.
2. Take the job lock.
3. Make a `pre-restore` backup of the current state, with files if the
   backup being restored includes files.
4. Enter maintenance mode (below).
5. `database.AutoMigrate` so the target has the current schema.
6. In **one transaction**:
   - Columns that are filled in later (self references, cycle breakers) are
     set to NULL first, because MySQL checks foreign keys row by row while
     deleting.
   - Delete all rows from every backed-up table and the three store tables,
     children before parents.
   - Insert rows parents before children, in batches of 500. The order comes
     from the foreign keys the target database reports (information_schema
     on Postgres and MySQL, `pragma_foreign_key_list` on SQLite),
     topologically sorted; ties keep `GetMigrationModels` order.
   - Self-references and cycles (today: `messages.reply_to_message_id`) are
     inserted with those columns NULL, then set with UPDATEs after the table
     is loaded.
   - Rows are inserted as column maps through `db.Table(name)`, with every
     column listed explicitly, so `false`, `0` and `""` are kept instead of
     being replaced by column defaults. No model hooks run; IDs and
     timestamps are kept exactly.
   - JSON values are decoded into each column's Go type before insert so the
     driver writes them the way it would for the app (e.g. JSON text on
     SQLite and MySQL, `jsonb` on Postgres, 0/1 vs boolean).
   - Commit. Any error rolls back and reports the table, row number and
     database message (e.g. a value too long for a MySQL column, or a broken
     reference).
7. If files are included: extract each area into a `.restore-<random>`
   folder inside the area, move the current entries into a `.old-<random>`
   folder inside the area, move the restored entries in, then remove both.
   Renames stay inside the folder, so this also works when the folder is a
   mount point. A failure here is reported with the pre-restore backup name
   to undo with.
8. Clear in-memory and store caches (permissions, settings, sessions) and
   resume background tasks; leave maintenance mode.

Postgres sequences need no reset: every backed-up table uses UUID keys (the
only auto-increment table, `scheduled_items`, is not backed up).

### Maintenance mode

- A process-wide flag in the handlers' `App`.
- While set, the global `Before` hook answers every request with
  `503 {"status":"error","message":"Restore in progress"}`, except
  `GET /api/maintenance` (public; returns `active` and the restore's phase,
  progress and whether it failed, never the error text) and the static
  frontend.
- Open WebSocket connections are closed; new ones are refused.
- In-process background tasks (SLA processor, chat delay processor, webhook
  delivery pruner, in-process campaign workers) are stopped through their
  contexts before step 5 and started again in step 8.
- Separate `whatomate worker` processes cannot be paused from the server; the
  restore dialog says to stop them first.
- The flag lives in memory only. A crash mid-restore leaves the transaction
  uncommitted, so a restart comes back to the old data and no maintenance
  mode.
- After a restore every user is sent to the login page and signs in with the
  accounts from the backup.

## Server

### Package layout

- `internal/backup`: the engine. No HTTP.
  - `Writer` / `Create(ctx, db, opts, progress) (Info, error)`
  - `Inspect(path, db) (Summary, error)`
  - `Restore(ctx, db, path, opts, progress) error`
  - `List(dir) ([]Info, error)`, name validation, temp cleanup.
  - `encode.go` / `decode.go` for the row encoding table above.
  - `order.go` for the foreign-key order.
- `internal/handlers/backup.go`: endpoints, job lock and progress state,
  maintenance flag wiring.
- `cmd/whatomate`: `backup` and `restore` commands.

### Endpoints (all super admin)

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/admin/backups` | List backups, the backup folder and the current job. |
| POST | `/api/admin/backups` | Start a backup. Body: `{"include_files": true}`. 409 if a job is running. |
| GET | `/api/admin/backup-job` | Current job progress. |
| GET | `/api/admin/backups/{name}/download` | Stream the zip. |
| POST | `/api/admin/backup-uploads` | Start an upload. Body: name, size. |
| PUT | `/api/admin/backup-uploads/{id}?offset=N` | One chunk of up to 8 MB. |
| POST | `/api/admin/backup-uploads/{id}/finish` | Finish the upload. |
| DELETE | `/api/admin/backup-uploads/{id}` | Cancel the upload. |
| GET | `/api/admin/backups/{name}/inspect` | Summary for the restore dialog. |
| POST | `/api/admin/backups/{name}/restore` | Body: `{"confirm":"RESTORE"}`. Starts the restore job. |
| DELETE | `/api/admin/backups/{name}` | Delete a backup. |
| GET | `/api/maintenance` | Public. While a restore runs (and for an hour after) returns its phase, progress and whether it failed — never the error text. |

`{name}` must match a file directly inside the backup folder (no separators,
no `..`); anything else is 404.

Uploads are sent in chunks of at most 8 MB, so the 15 MB request limit stays
in place for every other route; the total may not exceed
`[backup] max_upload_mb`. Downloads get a 6-hour write timeout through
fasthttp's `HeaderReceived` hook.

### CLI

```
whatomate backup  [-config f] [-no-files] [-o file]
whatomate restore [-config f] [-yes] <file>
```

- `backup` writes into the backup folder unless `-o` is given, printing
  progress like the migration bar.
- `restore` prints the inspect summary, asks to type `RESTORE` unless `-yes`,
  takes the pre-restore backup and restores. It refuses while a server is
  running against the same config.

To detect a running server, `whatomate server` writes `server.lock` (PID and
start time) in the backup folder at start and removes it on clean shutdown.
`whatomate restore` refuses when the PID in that file is alive, telling the
user to stop the server or use the web page. A stale file (dead PID) is
ignored and replaced.

## Frontend

- Route `/settings/backup`, `meta: { superAdmin: true }`, added to
  `navigation.ts` (Settings children, `superAdmin: true`, icon `Archive`) and
  `navigationOrder`.
- `views/settings/BackupView.vue` in the style of the other settings pages,
  dark and light themes, English strings in `en.json` under `backup.*`.
- **Top card:** "Back up now", "Include files" checkbox, backup folder path.
  While a job runs: progress bar, phase and current table.
- **List:** date, size, "made on" (version · driver), files yes/no, a
  `pre-restore` badge, actions Download / Restore / Delete. "Upload backup"
  above the table with upload progress.
- **Restore dialog:** inspect summary (source driver → this driver, counts,
  files), warnings (key mismatch in red, stop separate workers, everyone is
  signed out), the typed `RESTORE` field, then a progress view that polls
  `/api/maintenance`. When done it clears the session and goes to `/login`.
- Delete asks for confirmation.

## Error handling

- Every failure reaches the page as a clear message; the job state keeps the
  last error until the next job starts.
- Backup failure: temp file removed, nothing else changed.
- Restore failure before commit: database unchanged, maintenance mode left,
  background tasks resumed, message names the table and row.
- Restore failure while swapping files: database already restored; message
  names the pre-restore backup to restore from.
- Disk space: before a backup or upload, free space in the backup folder must
  exceed the estimated size (rows × average row size + file bytes); before a
  restore with files, free space must exceed the files' size. Otherwise
  refuse with the numbers.

## Testing

- `internal/backup` unit tests on SQLite (temp files):
  - Round trip: fill every backed-up table with rows covering JSON, Unicode,
    empty vs NULL, `false`/`0` columns that have non-zero defaults,
    soft-deleted rows, decimals, timestamps with nanoseconds, a reply chain in
    `messages`; back up; restore into a fresh database; compare every column
    of every row.
  - Refusals: bad checksum, unknown table, unknown column, higher `format`,
    path traversal in names.
  - Rollback: a row that violates a constraint leaves the target unchanged.
  - Files: include / exclude, staged swap, symlinks skipped.
- Cross-database tests (skipped when the database is not reachable):
  SQLite → Postgres (`whatomate_test` on 5433), Postgres → SQLite,
  SQLite → MySQL and MySQL → Postgres using the allowed MySQL test database.
- Handler tests: non-super-admins get 403 on every endpoint; name validation;
  409 when a job is running; maintenance mode returns 503 except
  `/api/maintenance`.
- Frontend: vitest for the restore dialog's confirm gating and the progress
  polling; vue-tsc and eslint.
- Manual: a throwaway instance (temp config, spare port, never the dev
  database) walked through backup, download, upload, restore and a SQLite →
  Postgres move, with screenshots of the page in both themes.

## Out of scope

- Scheduled / automatic backups and retention.
- Restoring a single organization or merging into existing data.
- Downloading S3 recordings into the backup.
- Encrypting the backup file itself.
- Restoring from the setup wizard.
