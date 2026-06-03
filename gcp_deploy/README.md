# Deploying planner to Cloud Run (Firestore backend)

planner runs on Cloud Run backed by Firestore. SQLite stays the default for local
use; Firestore is selected with `--backend firestore` (the Docker image defaults
to it via `PLANNER_BACKEND=firestore`).

## Prerequisites

- A GCP project with billing enabled.
- A **Firestore in Native mode** database (the default database id is `(default)`).
  Create it once: `gcloud firestore databases create --location=<region>`.
- `gcloud` authenticated (`gcloud auth login`) and the project set
  (`gcloud config set project <PROJECT_ID>`).
- These APIs enabled: `run.googleapis.com`, `firestore.googleapis.com`,
  `cloudbuild.googleapis.com`, `artifactregistry.googleapis.com`.

## 1. Create the composite indexes

The Firestore queries need three composite indexes (see
`firestore.indexes.json`). Without them, the first run of an un-indexed query
fails with a console link. Create them up front.

With the Firebase CLI (reads `firestore.indexes.json` directly):

```sh
firebase deploy --only firestore:indexes --project <PROJECT_ID>
```

Or with `gcloud` (one command per index). Add `--database=<DB>` for a non-default
database (e.g. `--database=planner`); omit it for `(default)`:

```sh
# versions: "latest version of a plan" (number DESC)
gcloud firestore indexes composite create \
  --collection-group=versions --query-scope=COLLECTION \
  --field-config=field-path=plan_id,order=ascending \
  --field-config=field-path=number,order=descending

# comments: ListComments ordering within a version
gcloud firestore indexes composite create \
  --collection-group=comments --query-scope=COLLECTION \
  --field-config=field-path=version_id,order=ascending \
  --field-config=field-path=line_start,order=ascending \
  --field-config=field-path=created_at,order=ascending

# comments: open-comment Count() per plan
gcloud firestore indexes composite create \
  --collection-group=comments --query-scope=COLLECTION \
  --field-config=field-path=plan_id,order=ascending \
  --field-config=field-path=status,order=ascending
```

Index builds take a few minutes; deploy can proceed in parallel.

## 2. Deploy

Build and deploy straight from source (Cloud Build uses the repo-root
`Dockerfile`; `.gcloudignore` controls what is uploaded):

```sh
gcloud run deploy planner \
  --source . \
  --region <REGION> \
  --set-env-vars PLANNER_BACKEND=firestore,PLANNER_FIRESTORE_PROJECT=<PROJECT_ID> \
  --no-allow-unauthenticated
```

Notes:
- **`PLANNER_FIRESTORE_PROJECT` is required** — the firestore backend refuses to
  start without a project (or `--project`).
- `PLANNER_FIRESTORE_DATABASE` is optional; it defaults to `(default)`.
- Do **not** set `--port`; Cloud Run injects `$PORT` and `planner serve` binds
  `0.0.0.0:$PORT`.
- The image already sets `PLANNER_BACKEND=firestore`; the env var above is
  belt-and-suspenders / makes the backend explicit.

## 3. Service account / IAM

The Cloud Run service runs as a service account that needs Firestore access.
Grant the runtime service account the **Cloud Datastore User** role
(`roles/datastore.user`), which covers Firestore Native reads/writes:

```sh
gcloud projects add-iam-policy-binding <PROJECT_ID> \
  --member=serviceAccount:<RUNTIME_SA_EMAIL> \
  --role=roles/datastore.user
```

(The default compute service account works for a quick start, but a dedicated,
least-privilege service account passed via `--service-account` is recommended.)

## ⚠️ Authentication / exposure

planner has **no authentication of its own**. Deploying with
`--allow-unauthenticated` would expose every plan and comment to anyone with the
URL. This deploy uses `--no-allow-unauthenticated`; reach the service through one
of:

- IAM-authenticated invokers + `gcloud run services proxy` for local access, or
- Identity-Aware Proxy (IAP) / an authenticating load balancer in front.

Adding an in-app auth layer is intentionally **out of scope** for this change.

## Local development

Nothing here changes local use — `planner serve` still defaults to SQLite:

```sh
planner serve                 # sqlite at ~/.planner/planner.db
planner serve --backend firestore --project <PROJECT_ID>   # talk to real Firestore
```

To run the backend-conformance tests against the Firestore emulator:

```sh
gcloud emulators firestore start --host-port=localhost:8081   # in one shell
export FIRESTORE_EMULATOR_HOST=localhost:8081
go test ./internal/store                                       # runs sqlite + firestore
```
