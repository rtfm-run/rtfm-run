# rtfm.run

An AI agent follows your quickstart on a fresh machine, the way a new developer would. When a step breaks, it finds the cause, rewrites the doc, has a second fresh agent follow the new version, and opens a pull request only if the fix works.

Live at [rtfm.run](https://rtfm.run).

| Path | What it is |
|---|---|
| `cmd/rtfm`, `internal/` | The `rtfm` Go binary: CLI, API server (`rtfm serve`) and workers (`rtfm worker`) |
| `web/` | The landing page and dashboard (Next.js) |
| `deploy/gcp/` | Production deploy on Google Cloud ([README](deploy/gcp/README.md)) |
| `examples/` | Two small quickstarts to try it on: one that works, one that's stale |

## How a run works

1. **Newhire**: an agent (the [Antigravity CLI](https://antigravity.google), `agy`, on Gemini) reads the doc and runs each step inside a blank, isolated box: a Firecracker microVM when KVM is available, otherwise a Docker container. Every command goes through an MCP exec server, so each step has a recorded exit code and output.
2. **Diagnose**: failed steps are classified (stale doc, missing prerequisite, needs credentials, code bug, …).
3. **Fix and verify**: a fixer agent edits the doc, then a new newhire follows the edited doc in a new box. The fix is kept only if the rerun has fewer problems; it's *partial* if some remain.
4. **Pull request**: the fix is drafted to the repo's PR template and CONTRIBUTING rules and discloses AI involvement. Projects that refuse AI contributions are never sent one.

## Requirements

- Go (see `go.mod`) and Docker
- `agy`, signed in (`agy` once, interactively)
- For microVMs: Linux with `/dev/kvm` and Firecracker; otherwise `--backend docker`
- For the service: Postgres and a GitHub App
- For the dashboard: Node.js 22

## CLI

```bash
go build -o bin/rtfm ./cmd/rtfm
bin/rtfm devbox build                             # once: the blank-slate box image
bin/rtfm run examples/stale-quickstart --goal "install the CLI and greet 'newhire'"
bin/rtfm run https://github.com/owner/repo --doc docs/quickstart.md
bin/rtfm sweep examples/targets.example.json      # many quickstarts, one summary
bin/rtfm pr <target>                              # preview the pull request for a verified fix
```

Run artifacts (logs, transcripts, patches) go to `runs/`. `bin/rtfm --help` lists every command, and `bin/rtfm run -h` its flags. Credentials a quickstart needs (API keys and the like) are saved with `rtfm creds set NAME` (a `0600` file under `~/.config/rtfm`) and handed only to the steps that ask for them; the service keeps them encrypted in the database.

## Running the service locally

The service is the API (`rtfm serve`), Postgres, workers that execute runs, and the dashboard in `web/`.

```bash
# Postgres
docker run -d --name rtfm-pg -p 127.0.0.1:55432:5432 \
  -e POSTGRES_USER=rtfm -e POSTGRES_PASSWORD=rtfm -e POSTGRES_DB=rtfm postgres:16

# Once: register a GitHub App (one click on GitHub); writes ~/.config/rtfm/service.env
bin/rtfm github-app create

# API + 3 workers on :8080 (migrations run on start; `rtfm migrate` applies them alone)
bin/rtfm serve --env-file ~/.config/rtfm/service.env --addr 127.0.0.1:8080 --workers 3

# Dashboard on :3000
cd web
echo 'RTFM_API_URL=http://localhost:8080' > .env.local
npm install
npm run dev
# then open http://localhost:3000/login
```

`service.env` holds `RTFM_DATABASE_URL`, `RTFM_SECRET_KEY`, `RTFM_BASE_URL`, `RTFM_DASHBOARD_URL` and the GitHub App's credentials. Settings in the environment override the file.

`--auto-pr` (on `serve` or `worker`) opens verified fixes as pull requests from the GitHub App without anyone clicking, skipping partial fixes and projects that refuse AI contributions. Without it, each draft waits on the run's page in the dashboard, where the person opening it answers anything the project requires (DCO sign-off, CLA, an issue link, template attestations).

On a GitHub App whose webhook URL has to change (a new tunnel, say): `bin/rtfm github-app webhook-url https://…/webhooks/github`.

### Connecting the dashboard to the API

The dashboard (`/dashboard`, `/login`) talks to the API straight from the browser, with the API's session cookie. `RTFM_API_URL` is read at request time, so no rebuild is needed, and `next dev` reloads `.env.local` on change. Without `RTFM_API_URL`, the dashboard shows read-only sample data (`web/src/lib/sample-api.ts`).

On the API side, `RTFM_DASHBOARD_URL` is the dashboard's `/dashboard` URL. The API uses it to:

- allow that origin to make credentialed CORS requests (`/api/*`, including the run event stream)
- send people there after sign-in, and back to `/login?error=…` when sign-in fails
- redirect its own `/` to the dashboard

The session cookie is `SameSite=Lax`, so the dashboard and the API must be on the same site: two ports on `localhost` in development, or `rtfm.run` and `api.rtfm.run` in production.

## Tests

```bash
go test ./...                                      # DB tests use RTFM_TEST_DATABASE_URL when set
cd web && npx tsc --noEmit && npm run lint
```

## Deploying

See [deploy/gcp/README.md](deploy/gcp/README.md). `deploy/gcp/deploy.sh images api front` rebuilds and ships the API and the dashboard.
