# Deploying rtfm.run on Google Cloud

| Piece | Runs on | Address |
|---|---|---|
| Dashboard (`front` repo) | Cloud Run `rtfm-front` | https://rtfm.run |
| API (`rtfm serve --workers 0`) | Cloud Run `rtfm-api` | https://api.rtfm.run |
| Workers (`rtfm worker`) | Compute Engine VM with nested virtualization (Firecracker needs KVM) | none |
| Postgres | Cloud SQL (Cloud Run uses its socket; the VM uses the Auth Proxy) | none |
| Secrets | Secret Manager | none |
| Agent models | `agy`, signed in once on the worker VM (it supports no API key or Vertex option) | none |

## First deploy

```bash
gcloud auth login
gcloud billing accounts list                       # pick the account
export BILLING_ACCOUNT=XXXXXX-XXXXXX-XXXXXX

deploy/gcp/deploy.sh project sql accounts

# Register the production GitHub App (one click on GitHub):
rtfm github-app create --base-url https://api.rtfm.run --dir ~/.config/rtfm/prod --name rtfm-run

deploy/gcp/deploy.sh secrets images api front domains
# → add the printed DNS records in Cloudflare (DNS only, not proxied)

deploy/gcp/deploy.sh worker

# Sign agy in on the worker, once (it opens a Google sign-in link):
gcloud compute ssh rtfm-worker-1 --zone us-central1-a -- -t sudo -u rtfm -H agy
```

`deploy.sh` reads `PROJECT` (default `rtfm-prod`), `REGION`, `DOMAIN`, `WORKER_TYPE`, `WORKERS`, `WORKER_FLAGS` (default `--auto-pr`) and others from the environment; see the top of the script.

## Updating

```bash
deploy/gcp/deploy.sh images api front   # API and dashboard
deploy/gcp/deploy.sh worker             # rebuilds and reinstalls rtfm on the VM
```

## Notes

- Domain mappings need the domain verified for your Google account once: `gcloud domains verify rtfm.run` (adds a TXT record in Cloudflare).
- Cloudflare records must be **DNS only**: Google issues the TLS certificates, and they can't be issued behind Cloudflare's proxy.
- The API and dashboard share the site `rtfm.run`, so the sign-in cookie (`SameSite=Lax`) works across `api.` and the apex.
- Stop the worker VM when idle to save money: `gcloud compute instances stop rtfm-worker-1 --zone us-central1-a`. Queued runs wait until it starts again.
- Secrets never go on command lines: values are piped to `gcloud secrets` and to the Cloud SQL Admin API. The VM fetches them with its service account at start (`rtfm-fetch-secrets`).
