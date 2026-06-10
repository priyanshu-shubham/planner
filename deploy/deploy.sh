#!/usr/bin/env bash
#
# planner deployer: from one env file,
#   1. provision a database + user for planner in a Cloud SQL (Postgres) instance,
#   2. build the image (frontend bundle + CLI matrix + Go binary) and push to
#      Artifact Registry,
#   3. create/update a Knative Serving Service on the CURRENT kubectl context.
#
# All steps are idempotent — re-running re-deploys cleanly. See deploy/README.md.
#
# Usage:
#   ./deploy/deploy.sh [--env FILE] [--skip-db] [--skip-build] [--skip-deploy] [--dry-run]
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
TEMPLATE="$SCRIPT_DIR/knative-service.yaml.tmpl"

ENV_FILE="$SCRIPT_DIR/planner.env"
SKIP_DB=false
SKIP_BUILD=false
SKIP_DEPLOY=false
DRY_RUN=false

die()  { echo "error: $*" >&2; exit 1; }
info() { echo "==> $*"; }

usage() {
  sed -n '2,15p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while [ $# -gt 0 ]; do
  case "$1" in
    --env)         ENV_FILE="${2:?--env needs a path}"; shift 2 ;;
    --env=*)       ENV_FILE="${1#*=}"; shift ;;
    --skip-db)     SKIP_DB=true; shift ;;
    --skip-build)  SKIP_BUILD=true; shift ;;
    --skip-deploy) SKIP_DEPLOY=true; shift ;;
    --dry-run)     DRY_RUN=true; shift ;;
    -h|--help)     usage 0 ;;
    *)             echo "unknown argument: $1" >&2; usage 1 ;;
  esac
done

# run CMD... — echo and execute, or just echo under --dry-run.
run() {
  if $DRY_RUN; then echo "  [dry-run] $*"; else "$@"; fi
}

# ensure_namespace — idempotent namespace upsert.
ensure_namespace() {
  if $DRY_RUN; then
    echo "  [dry-run] ensure namespace '$NAMESPACE'"
  else
    kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  fi
}

# upsert_secret NAME KEY=VAL [KEY=VAL ...] — idempotent Secret upsert. Under
# --dry-run it prints only the key names (never the secret values).
upsert_secret() {
  local name="$1"; shift
  local args=() keys=()
  local kv
  for kv in "$@"; do args+=(--from-literal="$kv"); keys+=("${kv%%=*}"); done
  if $DRY_RUN; then
    echo "  [dry-run] upsert secret '$name' in '$NAMESPACE' (keys: ${keys[*]})"
  else
    kubectl create secret generic "$name" -n "$NAMESPACE" "${args[@]}" \
      --dry-run=client -o yaml | kubectl apply -f -
  fi
}

# ---------------------------------------------------------------------------
# Config + preflight
# ---------------------------------------------------------------------------
[ -f "$ENV_FILE" ] || die "env file not found: $ENV_FILE (copy deploy/planner.env.example)"
# shellcheck disable=SC1090
set -a; . "$ENV_FILE"; set +a

need() { command -v "$1" >/dev/null 2>&1 || die "required tool not on PATH: $1"; }
require_var() { [ -n "${!1:-}" ] || die "missing required var in $ENV_FILE: $1"; }

need gcloud
$SKIP_BUILD  || need docker
$SKIP_DEPLOY || { need kubectl; need envsubst; }

for v in GCP_PROJECT NAMESPACE SERVICE_NAME AR_REGION AR_REPO IMAGE_NAME \
         CLOUDSQL_INSTANCE DB_NAME DB_USER DB_PASSWORD; do
  require_var "$v"
done

# Defaults / derivations.
: "${DB_SSLMODE:=require}"
: "${PLANNER_AUTH:=0}"
: "${MIN_SCALE:=1}"
: "${MAX_SCALE:=3}"
: "${CPU_REQUEST:=250m}"
: "${MEMORY_REQUEST:=256Mi}"
: "${MEMORY_LIMIT:=512Mi}"

if [ -z "${IMAGE_TAG:-}" ]; then
  IMAGE_TAG="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null)" \
    || die "IMAGE_TAG is empty and 'git rev-parse' failed; set IMAGE_TAG in $ENV_FILE"
fi

IMAGE="$AR_REGION-docker.pkg.dev/$GCP_PROJECT/$AR_REPO/$IMAGE_NAME:$IMAGE_TAG"
DB_SECRET_NAME="$SERVICE_NAME-db"
AUTH_SECRET_NAME="$SERVICE_NAME-auth"

if [ -z "${DB_HOST:-}" ]; then
  info "deriving private IP of Cloud SQL instance '$CLOUDSQL_INSTANCE'"
  DB_HOST="$(gcloud sql instances describe "$CLOUDSQL_INSTANCE" --project "$GCP_PROJECT" \
    --format='value(ipAddresses.filter("type:PRIVATE").extract(ipAddress).flatten())')" \
    || die "could not describe instance '$CLOUDSQL_INSTANCE'"
  [ -n "$DB_HOST" ] || die "instance '$CLOUDSQL_INSTANCE' has no PRIVATE IP; set DB_HOST explicitly"
fi

DSN="postgres://$DB_USER:$DB_PASSWORD@$DB_HOST:5432/$DB_NAME?sslmode=$DB_SSLMODE"
DSN_REDACTED="postgres://$DB_USER:***@$DB_HOST:5432/$DB_NAME?sslmode=$DB_SSLMODE"

info "project=$GCP_PROJECT  image=$IMAGE"
info "db=$DSN_REDACTED  auth=$PLANNER_AUTH"
$DRY_RUN && info "DRY RUN — no changes will be made"

# ---------------------------------------------------------------------------
# Phase 1: database + user (Cloud SQL Admin API — works against private-IP
# instances from anywhere; no DB connectivity needed). Cloud SQL auto-grants
# cloudsqlsuperuser to the created user, so planner can create its tables on boot.
# ---------------------------------------------------------------------------
db_phase() {
  info "database: ensuring '$DB_NAME' on instance '$CLOUDSQL_INSTANCE'"
  if gcloud sql databases describe "$DB_NAME" --instance "$CLOUDSQL_INSTANCE" \
       --project "$GCP_PROJECT" >/dev/null 2>&1; then
    echo "  database '$DB_NAME' already exists"
  else
    run gcloud sql databases create "$DB_NAME" --instance "$CLOUDSQL_INSTANCE" \
      --project "$GCP_PROJECT"
  fi

  info "database: ensuring user '$DB_USER'"
  if gcloud sql users list --instance "$CLOUDSQL_INSTANCE" --project "$GCP_PROJECT" \
       --format='value(name)' | grep -qx "$DB_USER"; then
    echo "  user '$DB_USER' already exists (password left unchanged)"
  else
    run gcloud sql users create "$DB_USER" --instance "$CLOUDSQL_INSTANCE" \
      --project "$GCP_PROJECT" --password "$DB_PASSWORD"
  fi
}

# ---------------------------------------------------------------------------
# Phase 2: build + push image. --platform linux/amd64 so an Apple-Silicon build
# still runs on amd64 GKE nodes. Docker is assumed already authed to AR.
# ---------------------------------------------------------------------------
build_phase() {
  info "image: ensuring Artifact Registry repo '$AR_REPO' in '$AR_REGION'"
  if gcloud artifacts repositories describe "$AR_REPO" --location "$AR_REGION" \
       --project "$GCP_PROJECT" >/dev/null 2>&1; then
    echo "  repo '$AR_REPO' already exists"
  else
    run gcloud artifacts repositories create "$AR_REPO" --location "$AR_REGION" \
      --project "$GCP_PROJECT" --repository-format docker
  fi

  info "image: building $IMAGE (linux/amd64)"
  run docker build --platform linux/amd64 -t "$IMAGE" "$REPO_ROOT"
  info "image: pushing"
  run docker push "$IMAGE"
}

# ---------------------------------------------------------------------------
# Phase 3: deploy the Knative Service to the CURRENT kubectl context.
# ---------------------------------------------------------------------------
deploy_phase() {
  local ctx; ctx="$(kubectl config current-context 2>/dev/null || echo '<none>')"
  info "deploy: target kubectl context = '$ctx'  namespace = '$NAMESPACE'"
  if ! $DRY_RUN; then
    echo "  applying in 4s — Ctrl-C now if that context is wrong"; sleep 4
  fi

  if ! kubectl get crd services.serving.knative.dev >/dev/null 2>&1; then
    $DRY_RUN && echo "  [dry-run] warning: Knative CRD not found on '$ctx' (or cluster unreachable)" \
             || die "Knative Serving not installed on context '$ctx' (no services.serving.knative.dev CRD)"
  fi

  info "deploy: ensuring namespace '$NAMESPACE'"
  ensure_namespace

  info "deploy: upserting DSN secret '$DB_SECRET_NAME'"
  upsert_secret "$DB_SECRET_NAME" dsn="$DSN"

  # Auth: emit the env entries (fully expanded) and a Secret only when enabled.
  AUTH_ENV_BLOCK=""
  if [ "$PLANNER_AUTH" = "1" ]; then
    for v in GOOGLE_CLIENT_ID GOOGLE_CLIENT_SECRET; do
      require_var "$v"
    done
    info "deploy: upserting auth secret '$AUTH_SECRET_NAME'"
    upsert_secret "$AUTH_SECRET_NAME" \
      google_client_id="$GOOGLE_CLIENT_ID" \
      google_client_secret="$GOOGLE_CLIENT_SECRET" \
      auth_secret="${PLANNER_AUTH_SECRET:-}"
    AUTH_ENV_BLOCK="$(cat <<EOF
            - name: PLANNER_AUTH
              value: "1"
            - name: GOOGLE_CLIENT_ID
              valueFrom:
                secretKeyRef:
                  name: $AUTH_SECRET_NAME
                  key: google_client_id
            - name: GOOGLE_CLIENT_SECRET
              valueFrom:
                secretKeyRef:
                  name: $AUTH_SECRET_NAME
                  key: google_client_secret
            - name: PLANNER_AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: $AUTH_SECRET_NAME
                  key: auth_secret
EOF
)"
  fi

  info "deploy: rendering and applying Knative Service '$SERVICE_NAME'"
  local manifest
  manifest="$(SERVICE_NAME="$SERVICE_NAME" NAMESPACE="$NAMESPACE" IMAGE="$IMAGE" \
    DB_SECRET_NAME="$DB_SECRET_NAME" AUTH_ENV_BLOCK="$AUTH_ENV_BLOCK" \
    PLANNER_BASE_URL="${PLANNER_BASE_URL:-}" \
    MIN_SCALE="$MIN_SCALE" MAX_SCALE="$MAX_SCALE" CPU_REQUEST="$CPU_REQUEST" \
    MEMORY_REQUEST="$MEMORY_REQUEST" MEMORY_LIMIT="$MEMORY_LIMIT" \
    envsubst '$SERVICE_NAME $NAMESPACE $IMAGE $DB_SECRET_NAME $AUTH_ENV_BLOCK $PLANNER_BASE_URL $MIN_SCALE $MAX_SCALE $CPU_REQUEST $MEMORY_REQUEST $MEMORY_LIMIT' \
    < "$TEMPLATE")"

  if $DRY_RUN; then
    echo "----- rendered manifest -----"; echo "$manifest"; echo "-----------------------------"
  else
    echo "$manifest" | kubectl apply -f -
    info "deploy: waiting for the revision to become Ready"
    kubectl wait --for=condition=Ready "ksvc/$SERVICE_NAME" -n "$NAMESPACE" --timeout=180s || true
    local url; url="$(kubectl get ksvc "$SERVICE_NAME" -n "$NAMESPACE" -o jsonpath='{.status.url}' 2>/dev/null || true)"
    info "deployed: ${url:-<url not ready yet; check 'kubectl get ksvc $SERVICE_NAME -n $NAMESPACE'>}"
  fi
}

$SKIP_DB     || db_phase
$SKIP_BUILD  || build_phase
$SKIP_DEPLOY || deploy_phase
info "done."
