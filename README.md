# PocketBase

[![Deploy on velixir](https://velixir.net/img/deploy-on-velixir.svg)](https://velixir.net/new?template=pocketbase)

A whole backend for your web or mobile app, running in a few minutes: a database you design in a
dashboard, a REST API with access rules, email and OAuth2 sign-in, file uploads and realtime
subscriptions. [PocketBase](https://pocketbase.io) does all of it from one small process, and this
template runs it in the EU with your data on a persistent volume.

[Deploy it on velixir](https://velixir.net/new?template=pocketbase), open the deploy log for your
password, and sign in to the dashboard.

## What you get

- **PocketBase 0.40.5**, the upstream release, with the dashboard at `/_/` and the API at `/api/`.
- **Your data kept safe.** The template adds a persistent volume and keeps `pb_data` (the SQLite
  database and every uploaded file) on it, so it survives restarts and redeploys. Snapshots are
  taken daily and can be restored from the app's Volume tab.
- **An instance only you can sign in to.** A fresh PocketBase prints a one-time link for whoever
  opens it first to create the superuser. This template creates the superuser itself on the first
  start and prints the password once to the deploy log, so there is nothing to race for.
- **Everything the official binary does.** JS hooks in `pb_hooks/`, JS migrations in
  `pb_migrations/` and your own front end in `pb_public/`, from this repository.
- **Real visitor addresses** in PocketBase's logs and rate limits from the first start, and the
  app's public URL already filled in for emails and OAuth2 redirects.
- **Room to grow into Go.** PocketBase is built here as a Go framework, the way upstream recommends
  extending it, so custom routes, hooks and jobs in Go are a few lines in `main.go`.

## Signing in

After the first deploy, open the app's log and find the box that begins **PocketBase is ready**.
It holds the dashboard address, the superuser email (`admin@` your app's host) and a random
password. It is printed once. Sign in at `/_/`, then change the email and password under
**Superusers**.

Lost it? Set `PB_ADMIN_EMAIL` and `PB_ADMIN_PASSWORD` on the app's Environment tab (mark the
password as secret). On every start the app creates that superuser or resets its password, so
saving them (which redeploys) gets you back in. Remove them again afterwards if you prefer to
manage the account in the dashboard.

| Variable | Required | What it does |
| --- | --- | --- |
| `PB_ADMIN_EMAIL` and `PB_ADMIN_PASSWORD` | no | Create this superuser, or reset its password, on every start. The password needs at least 10 characters. |
| `PUBLIC_URL` | no | Your own domain, for example `https://api.example.com`, once you have added it to the app. Used for the app URL in PocketBase's settings while that still has its default. |
| `PB_AUTOMIGRATE` | no | `true` to have PocketBase write migration files when you change collections (off by default, see below). |

Everything else (mail, S3 storage, OAuth2 providers, backups, rate limits) is set in the dashboard
under **Settings** and stored in the database on the volume.

## Using it from your app

Point an SDK at your app's address:

```js
import PocketBase from 'pocketbase';
const pb = new PocketBase('https://your-app.velixir.run');
const posts = await pb.collection('posts').getList(1, 20);
```

There are official SDKs for [JavaScript and Dart](https://pocketbase.io/docs/client-side-sdks/),
and the API is plain HTTP for anything else. Realtime subscriptions use server-sent events, which
work through velixir's edge.

## Extending it

- **JS hooks**: add `*.pb.js` files to `pb_hooks/` and redeploy. See
  [Extend with JavaScript](https://pocketbase.io/docs/js-overview/).
- **Go**: add routes and hooks in `main.go` before `app.Start()`. See
  [Extend with Go](https://pocketbase.io/docs/go-overview/).
- **Your front end**: replace `pb_public/` with your built site. Unknown paths fall back to
  `index.html`, so a single-page app works as it is.
- **Migrations**: commit them to `pb_migrations/`. Automatic migration files are off here because
  files PocketBase writes at runtime land on the container disk and vanish on the next deploy; the
  schema itself is always in the database on the volume. To generate migration files, run it on
  your machine with `PB_AUTOMIGRATE=true` and commit what it writes.

## Things to know

**It needs a paid plan.** Volumes come with paid plans, and on the Free plan the app would sleep
when idle and start over with an empty database each time, so this template rules Free out. It
idles at about 15 MB of memory, so the smallest paid plan has plenty of room.

**Without a volume, everything resets.** If you untick the volume, PocketBase still runs, but the
database, uploads and the superuser start from scratch on every restart and redeploy. The log says
so on every start.

**One replica.** SQLite on a volume means one running copy. PocketBase is built for that: one
small server handles a lot of traffic.

**Email needs an HTTPS-friendly provider.** velixir apps can reach ports 80 and 443 only, so
PocketBase's SMTP settings will not connect to port 25, 465 or 587. Use a provider whose SMTP
also listens on 443, or send mail from a hook through an HTTP API (Resend, Postmark, Brevo, SES).

**Large files.** Uploads are stored on the volume and count towards its size. For big media,
switch file storage to an S3-compatible bucket under **Settings > Files storage**.

**Health check:** `/healthz` answers 200 (PocketBase's own `/api/health` works too).

## What this repo is

A thin wrapper, not a fork. `go.mod` pins `github.com/pocketbase/pocketbase` to an exact release,
and `main.go` is upstream's own `examples/base` (the official binary) plus what running on velixir
needs:

| velixir provides | PocketBase gets |
| --- | --- |
| `PORT` | `serve --http=0.0.0.0:$PORT` when started with no arguments |
| `VELIXIR_VOLUME_PATH` | `pb_data` on the volume (or the container disk, with a warning) |
| `VELIXIR_APP_SLUG` / `PUBLIC_URL` | the app URL in settings, and the first superuser's email domain |
| the edge's `X-Real-IP` | trusted as the client address |
| nothing | a superuser created on the first start, `/healthz`, no self-update command |

`nixpacks.toml` installs Go 1.27 (PocketBase 0.40 needs it) from a pinned nixpkgs snapshot and
builds a static binary.

To upgrade PocketBase, change its version in `go.mod`, run `go mod tidy` (Go 1.27 or newer) and
deploy. Read the [changelog](https://github.com/pocketbase/pocketbase/blob/master/CHANGELOG.md)
first: PocketBase is still before 1.0 and minor versions can change the API.

## Running it locally

```bash
go run .
```

Then open http://localhost:8090/_/ and sign in with the password printed in the terminal. Data
goes in `pb_data/` in this folder. Any PocketBase command works too, for example
`go run . superuser upsert you@example.com a-long-password`.

## Upstream

PocketBase is MIT-licensed open source by Gani Georgiev. This wrapper is MIT too.

- Docs: https://pocketbase.io/docs/
- Source: https://github.com/pocketbase/pocketbase
