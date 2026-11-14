#!/bin/bash
# Deploys rtfm.run to Google Cloud:
#   rtfm.run       → Cloud Run service rtfm-front (Next.js dashboard)
#   api.rtfm.run   → Cloud Run service rtfm-api   (rtfm serve --workers 0)
#   worker VM      → Compute Engine with nested virtualization (rtfm worker)
#   Postgres       → Cloud SQL;  secrets → Secret Manager;  agents → agy on the worker
#
# Usage: deploy/gcp/deploy.sh <step> [...]
# Steps, in order for a first deploy (each is safe to rerun):
#   project   create the project, link billing, enable APIs
#   sql       Cloud SQL instance, database and user
#   secrets   load the production GitHub App's credentials into Secret Manager
#   accounts  service accounts and their roles
#   images    build and push both container images
#   api       deploy the API to Cloud Run
#   front     deploy the dashboard to Cloud Run
#   worker    create the worker VM and install rtfm on it
#   worker-bin  update just the rtfm binary and config on the worker, then restart it
#   domains   map rtfm.run and api.rtfm.run and print the DNS records
#   status    show what's deployed
set -euo pipefail

PROJECT=${PROJECT:-rtfm-prod}
REGION=${REGION:-us-central1}
ZONE=${ZONE:-us-central1-a}
DOMAIN=${DOMAIN:-rtfm.run}
API_DOMAIN=${API_DOMAIN:-api.$DOMAIN}
SQL_INSTANCE=${SQL_INSTANCE:-rtfm-pg}
SQL_TIER=${SQL_TIER:-db-g1-small}
DB_NAME=${DB_NAME:-rtfm}
DB_USER=${DB_USER:-rtfm}
REPO=${REPO:-rtfm}
WORKER_VM=${WORKER_VM:-rtfm-worker-1}
WORKER_TYPE=${WORKER_TYPE:-n2-standard-8}
WORKER_DISK_GB=${WORKER_DISK_GB:-200}
WORKERS=${WORKERS:-3}
WORKER_FLAGS=${WORKER_FLAGS:---auto-pr --timeout 10m}
# The production GitHub App, registered with:
#   rtfm github-app create --base-url https://api.rtfm.run --dir ~/.config/rtfm/prod --name rtfm-run
APP_DIR=${APP_DIR:-$HOME/.config/rtfm/prod}
AGY_BIN=${AGY_BIN:-$(command -v agy || true)}

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
FRONT=${FRONT:-$ROOT/web}
IMAGES="$REGION-docker.pkg.dev/$PROJECT/$REPO"
CONN="$PROJECT:$REGION:$SQL_INSTANCE"
G="gcloud --project $PROJECT --quiet"

say() { printf '\n▶ %s\n' "$*" >&2; }
exists() { "$@" >/dev/null 2>&1; }

# put_secret NAME < value: creates the secret or adds a version if the value
# changed. Values are read from stdin, never from the command line.
put_secret() {
  local name=$1 val
  val=$(cat)
  if ! exists $G secrets describe "$name"; then
    $G secrets create "$name" --replication-policy automatic >/dev/null
  elif [ "$($G secrets versions access latest --secret "$name" 2>/dev/null || true)" = "$val" ]; then
    return 0
  fi
  printf '%s' "$val" | $G secrets versions add "$name" --data-file=- >/dev/null
  echo "  secret $name updated" >&2
}

app_env() { # app_env KEY: a value from the production App's service.env
  sed -n "s/^$1=//p" "$APP_DIR/service.env" | head -1
}

step_project() {
  say "project $PROJECT"
  if ! exists gcloud projects describe "$PROJECT"; then
    gcloud projects create "$PROJECT" --name "rtfm run"
  fi
  if [ -n "${BILLING_ACCOUNT:-}" ]; then
    gcloud billing projects link "$PROJECT" --billing-account "$BILLING_ACCOUNT" >/dev/null
  elif [ "$(gcloud billing projects describe "$PROJECT" --format='value(billingEnabled)')" != "True" ]; then
    echo "Billing is not enabled. Find your account with 'gcloud billing accounts list', then rerun with BILLING_ACCOUNT=XXXXXX-XXXXXX-XXXXXX." >&2
    exit 1
  fi
  $G services enable run.googleapis.com sqladmin.googleapis.com secretmanager.googleapis.com \
    artifactregistry.googleapis.com compute.googleapis.com aiplatform.googleapis.com iam.googleapis.com
  exists $G artifacts repositories describe "$REPO" --location "$REGION" ||
    $G artifacts repositories create "$REPO" --location "$REGION" --repository-format docker
}

step_sql() {
  say "Cloud SQL $SQL_INSTANCE ($SQL_TIER)"
  exists $G sql instances describe "$SQL_INSTANCE" ||
    $G sql instances create "$SQL_INSTANCE" --database-version POSTGRES_16 --edition ENTERPRISE \
      --tier "$SQL_TIER" --region "$REGION" --storage-auto-increase --backup-start-time 08:00
  exists $G sql databases describe "$DB_NAME" --instance "$SQL_INSTANCE" ||
    $G sql databases create "$DB_NAME" --instance "$SQL_INSTANCE"
  if ! exists $G secrets describe rtfm-db-password; then
    openssl rand -base64 24 | tr -d '/+=\n' | put_secret rtfm-db-password
  fi
  local pw
  pw=$($G secrets versions access latest --secret rtfm-db-password)
  # Set the password through the Admin API with the body on stdin, so it never
  # appears on a command line.
  local api="https://sqladmin.googleapis.com/v1/projects/$PROJECT/instances/$SQL_INSTANCE/users" method=POST
  exists $G sql users describe "$DB_USER" --instance "$SQL_INSTANCE" && { method=PUT; api="$api?name=$DB_USER"; }
  printf '{"name":"%s","password":"%s"}' "$DB_USER" "$pw" |
    curl -fsS -X "$method" -H @<(printf 'Authorization: Bearer %s\n' "$(gcloud auth print-access-token)") \
      -H 'Content-Type: application/json' --data-binary @- "$api" >/dev/null
  # Cloud Run reaches Cloud SQL over its built-in unix socket.
  printf 'postgres://%s:%s@/%s?host=/cloudsql/%s' "$DB_USER" "$pw" "$DB_NAME" "$CONN" | put_secret rtfm-database-url
}

step_secrets() {
  say "secrets from $APP_DIR"
  [ -f "$APP_DIR/service.env" ] && [ -f "$APP_DIR/github-app.pem" ] || {
    echo "No production App in $APP_DIR. Register it first:" >&2
    echo "  rtfm github-app create --base-url https://$API_DOMAIN --dir $APP_DIR --name rtfm-run" >&2
    exit 1
  }
  for k in ID SLUG CLIENT_ID CLIENT_SECRET WEBHOOK_SECRET; do
    app_env "GITHUB_APP_$k" | put_secret "rtfm-github-app-$(echo "$k" | tr 'A-Z_' 'a-z-')"
  done
  put_secret rtfm-github-app-key < "$APP_DIR/github-app.pem"
  # The production encryption key for stored credentials and tokens. Never
  # replaced once set: rotating it would make stored secrets unreadable.
  exists $G secrets describe rtfm-secret-key || app_env RTFM_SECRET_KEY | put_secret rtfm-secret-key
}

step_accounts() {
  say "service accounts"
  local sa
  for sa in rtfm-api rtfm-worker; do
    exists $G iam service-accounts describe "$sa@$PROJECT.iam.gserviceaccount.com" ||
      $G iam service-accounts create "$sa" --display-name "$sa"
  done
  bind() { $G projects add-iam-policy-binding "$PROJECT" --member "serviceAccount:$1@$PROJECT.iam.gserviceaccount.com" --role "$2" --condition None >/dev/null; }
  bind rtfm-api roles/cloudsql.client
  bind rtfm-api roles/secretmanager.secretAccessor
  bind rtfm-worker roles/cloudsql.client
  bind rtfm-worker roles/secretmanager.secretAccessor
  bind rtfm-worker roles/aiplatform.user
  bind rtfm-worker roles/logging.logWriter
}

step_images() {
  say "images → $IMAGES"
  gcloud auth configure-docker "$REGION-docker.pkg.dev" --quiet >/dev/null
  local tag
  tag=$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || date +%s)
  docker build --platform linux/amd64 -t "$IMAGES/api:$tag" -t "$IMAGES/api:latest" "$ROOT"
  docker push -q "$IMAGES/api:$tag" && docker push -q "$IMAGES/api:latest"
  docker build --platform linux/amd64 -t "$IMAGES/front:$tag" -t "$IMAGES/front:latest" "$FRONT"
  docker push -q "$IMAGES/front:$tag" && docker push -q "$IMAGES/front:latest"
}

step_api() {
  say "Cloud Run rtfm-api"
  $G run deploy rtfm-api --image "$IMAGES/api:latest" --region "$REGION" \
    --service-account "rtfm-api@$PROJECT.iam.gserviceaccount.com" \
    --add-cloudsql-instances "$CONN" \
    --set-env-vars "RTFM_BASE_URL=https://$API_DOMAIN,RTFM_DASHBOARD_URL=https://$DOMAIN/dashboard" \
    --set-secrets "RTFM_DATABASE_URL=rtfm-database-url:latest,RTFM_SECRET_KEY=rtfm-secret-key:latest,GITHUB_APP_ID=rtfm-github-app-id:latest,GITHUB_APP_SLUG=rtfm-github-app-slug:latest,GITHUB_APP_CLIENT_ID=rtfm-github-app-client-id:latest,GITHUB_APP_CLIENT_SECRET=rtfm-github-app-client-secret:latest,GITHUB_APP_WEBHOOK_SECRET=rtfm-github-app-webhook-secret:latest,GITHUB_APP_PRIVATE_KEY=rtfm-github-app-key:latest" \
    --allow-unauthenticated --timeout 3600 --memory 512Mi --min-instances 0 --max-instances 4
}

step_front() {
  say "Cloud Run rtfm-front"
  $G run deploy rtfm-front --image "$IMAGES/front:latest" --region "$REGION" \
    --set-env-vars "RTFM_API_URL=https://$API_DOMAIN" \
    --allow-unauthenticated --memory 512Mi --min-instances 0 --max-instances 4
}

step_worker() {
  say "worker VM $WORKER_VM ($WORKER_TYPE)"
  [ -x "$AGY_BIN" ] || { echo "Set AGY_BIN to the agy binary to install on the worker." >&2; exit 1; }
  if ! exists $G compute instances describe "$WORKER_VM" --zone "$ZONE"; then
    $G compute instances create "$WORKER_VM" --zone "$ZONE" --machine-type "$WORKER_TYPE" \
      --enable-nested-virtualization --min-cpu-platform "Intel Cascade Lake" \
      --image-family ubuntu-2404-lts-amd64 --image-project ubuntu-os-cloud \
      --boot-disk-size "${WORKER_DISK_GB}GB" --boot-disk-type pd-balanced \
      --service-account "rtfm-worker@$PROJECT.iam.gserviceaccount.com" --scopes cloud-platform \
      --shielded-secure-boot
    echo "  waiting for SSH…" >&2
    for _ in $(seq 1 30); do $G compute ssh "$WORKER_VM" --zone "$ZONE" --command true >/dev/null 2>&1 && break; sleep 10; done
  fi
  local stage
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' RETURN
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$stage/rtfm" ./cmd/rtfm)
  cp "$AGY_BIN" "$stage/agy"
  cp "$HERE"/worker/* "$stage/"
  cat > "$stage/deploy.env" <<ENV
PROJECT=$PROJECT
REGION=$REGION
BASE_URL=https://$API_DOMAIN
DASHBOARD_URL=https://$DOMAIN/dashboard
DB_USER=$DB_USER
DB_NAME=$DB_NAME
RTFM_WORKERS=$WORKERS
RTFM_WORKER_FLAGS="$WORKER_FLAGS"
ENV
  echo "INSTANCE_CONNECTION_NAME=$CONN" > "$stage/proxy.env"
  $G compute ssh "$WORKER_VM" --zone "$ZONE" --command "rm -rf ~/rtfm-install && mkdir -p ~/rtfm-install"
  $G compute scp --zone "$ZONE" --recurse "$stage"/* "$WORKER_VM:~/rtfm-install/"
  $G compute ssh "$WORKER_VM" --zone "$ZONE" --command "sudo bash ~/rtfm-install/setup.sh"
}

step_worker_bin() {
  say "update rtfm on $WORKER_VM"
  local stage
  stage=$(mktemp -d)
  trap 'rm -rf "$stage"' RETURN
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$stage/rtfm" ./cmd/rtfm)
  $G compute scp --zone "$ZONE" "$stage/rtfm" "$WORKER_VM:~/rtfm-install/rtfm"
  # Wait for running jobs: systemd sends SIGINT and gives them TimeoutStopSec.
  $G compute ssh "$WORKER_VM" --zone "$ZONE" --command "sudo install -m 0755 ~/rtfm-install/rtfm /usr/local/bin/rtfm && sudo sed -i 's|^RTFM_WORKER_FLAGS=.*|RTFM_WORKER_FLAGS=\"$WORKER_FLAGS\"|' /etc/rtfm/deploy.env && sudo systemctl restart rtfm-worker && systemctl is-active rtfm-worker"
}

step_domains() {
  say "domain mappings"
  local d svc
  for pair in "$DOMAIN:rtfm-front" "$API_DOMAIN:rtfm-api"; do
    d=${pair%%:*}; svc=${pair#*:}
    exists $G beta run domain-mappings describe --domain "$d" --region "$REGION" ||
      $G beta run domain-mappings create --service "$svc" --domain "$d" --region "$REGION"
  done
  echo "Add these records in Cloudflare (DNS only, grey cloud, not proxied; an empty name means the apex @):" >&2
  for d in "$DOMAIN" "$API_DOMAIN"; do
    $G beta run domain-mappings describe --domain "$d" --region "$REGION" \
      --flatten status.resourceRecords --format "value(status.resourceRecords.type,status.resourceRecords.name,status.resourceRecords.rrdata)"
  done
}

step_status() {
  $G run services list --region "$REGION" --format "table(metadata.name,status.url,status.conditions[0].status)"
  $G compute instances list --format "table(name,zone,machineType.basename(),status)"
  $G sql instances list --format "table(name,databaseVersion,settings.tier,state)"
  $G beta run domain-mappings list --region "$REGION" 2>/dev/null || true
}

[ $# -gt 0 ] || { sed -n '2,22p' "$0"; exit 2; }
for s in "$@"; do
  fn="step_${s//-/_}"
  type "$fn" >/dev/null 2>&1 || { echo "unknown step: $s" >&2; exit 2; }
  "$fn"
done
