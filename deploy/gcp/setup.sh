#!/usr/bin/env bash
# One-time GCP setup for Code Runner's cloud delivery (see README.md in this folder).
# Run by someone allowed to administer PROJECT_ID, logged in with their own account
# (gcloud auth login). Safe to re-run: what already exists is kept.
#
#   PROJECT_ID=<project> ./deploy/gcp/setup.sh          # DRY_RUN=1 prints, runs nothing
#
# Creates in PROJECT_ID / REGION:
#   - Artifact Registry Docker repository REPO   (CRN_MIRROR_REGISTRY points here),
#     keeping the 5 newest versions of each image
#   - Pub/Sub topics "gcr" (Artifact Registry announces images on it) and "crn-sweep"
#     (Cloud Scheduler publishes on it every 5 minutes)
#   - service accounts:
#       crn-pusher        writes images to REPO (used by the Code Runner build machine)
#       crn-uat-deployer  runs both triggers: registers cloud projects (their own
#                         runtime accounts crn-uat-<id8>, databases, secrets, buckets,
#                         scheduler jobs) and deploys them. It may grant only
#                         roles/cloudsql.client on the project and administer only
#                         crn-uat-*/crn-prod-* service accounts.
#   - Cloud SQL for PostgreSQL 16 instance SQL_INSTANCE, shared by UAT projects (one
#     database each), and its postgres password in secret crn-uat-postgres-password.
#     It runs all month — see the cost estimate before creating it.
#   - secret crn-deploy-fb-token: FITT Builder's CODE_RUNNER credential, typed at the
#     prompt (issue it with FITT Builder's scripts/deploy-binding.mjs)
#   - Cloud Build triggers crn-uat-deploy (topic gcr) and crn-uat-sweep (topic
#     crn-sweep), both running cloudbuild-uat.yaml
#
# It creates no key for crn-pusher: issuing a credential is a separate, deliberate
# step (README.md, "Build machine").
set -euo pipefail
: "${PROJECT_ID:?set PROJECT_ID}"
REGION="${REGION:-asia-southeast1}"
REPO="${REPO:-crn}"
SQL_INSTANCE="${SQL_INSTANCE:-crn-uat}"
SQL_TIER="${SQL_TIER:-db-custom-1-3840}"
STATUS_URL="${STATUS_URL:-https://fitt-builder.fittbsa.com/v1/service-status}"
FB_URL="${FB_URL:-https://fitt-builder.fittbsa.com}"
here="$(cd "$(dirname "$0")" && pwd)"

run() { echo "+ $*"; if [ -z "${DRY_RUN:-}" ]; then "$@"; fi; }
# create-if-missing: a resource that already exists is not an error on a re-run.
keep() { run "$@" || echo "  (kept as it is: it already exists)"; }
sa() { echo "$1@${PROJECT_ID}.iam.gserviceaccount.com"; }
deployer="$(sa crn-uat-deployer)"

run gcloud config set project "${PROJECT_ID}"
run gcloud services enable artifactregistry.googleapis.com run.googleapis.com sqladmin.googleapis.com \
  secretmanager.googleapis.com cloudbuild.googleapis.com pubsub.googleapis.com iam.googleapis.com \
  cloudscheduler.googleapis.com storage.googleapis.com logging.googleapis.com
keep gcloud artifacts repositories create "${REPO}" --repository-format=docker --location="${REGION}" \
  --description="Code Runner images, mirrored with their digests unchanged"
cleanup="$(mktemp)"
cat > "${cleanup}" <<'JSON'
[{"name": "keep-newest-5", "action": {"type": "Keep"}, "mostRecentVersions": {"keepCount": 5}},
 {"name": "delete-older", "action": {"type": "Delete"}, "condition": {"tagState": "any", "olderThan": "7d"}}]
JSON
run gcloud artifacts repositories set-cleanup-policies "${REPO}" --location="${REGION}" --policy="${cleanup}" --no-dry-run
keep gcloud pubsub topics create gcr
keep gcloud pubsub topics create crn-sweep

keep gcloud iam service-accounts create crn-pusher --display-name="Code Runner image pusher"
keep gcloud iam service-accounts create crn-uat-deployer --display-name="Code Runner deploy side (Cloud Build)"

run gcloud artifacts repositories add-iam-policy-binding "${REPO}" --location="${REGION}" \
  --member="serviceAccount:$(sa crn-pusher)" --role=roles/artifactregistry.writer
run gcloud artifacts repositories add-iam-policy-binding "${REPO}" --location="${REGION}" \
  --member="serviceAccount:${deployer}" --role=roles/artifactregistry.reader
for role in roles/run.admin roles/secretmanager.admin roles/cloudsql.admin roles/storage.admin \
  roles/cloudscheduler.admin roles/iam.serviceAccountCreator roles/logging.logWriter roles/logging.viewer; do
  run gcloud projects add-iam-policy-binding "${PROJECT_ID}" --condition=None \
    --member="serviceAccount:${deployer}" --role="${role}"
done
# Granting roles on the project, limited to the one role each app's account needs.
run gcloud projects add-iam-policy-binding "${PROJECT_ID}" --member="serviceAccount:${deployer}" \
  --role=roles/resourcemanager.projectIamAdmin \
  "--condition=^;^title=crn-grants-cloudsql-client-only;expression=api.getAttribute('iam.googleapis.com/modifiedGrantsByRole', []).hasOnly(['roles/cloudsql.client'])"
# Administering service accounts, limited to the apps' own (crn-uat-*, crn-prod-*).
run gcloud projects add-iam-policy-binding "${PROJECT_ID}" --member="serviceAccount:${deployer}" \
  --role=roles/iam.serviceAccountAdmin \
  "--condition=^;^title=crn-app-accounts-only;expression=resource.name.extract('serviceAccounts/{account}@').startsWith('crn-uat-') || resource.name.extract('serviceAccounts/{account}@').startsWith('crn-prod-')"

keep gcloud sql instances create "${SQL_INSTANCE}" --database-version=POSTGRES_16 --edition=ENTERPRISE \
  --tier="${SQL_TIER}" --region="${REGION}" --storage-type=SSD --storage-size=10 \
  --backup-start-time=19:00 --retained-backups-count=7
# The deploy side closes each project's database as postgres. gcloud takes the
# password only as a flag, so the printed command hides it.
if [ -n "${DRY_RUN:-}" ] || ! gcloud secrets describe crn-uat-postgres-password >/dev/null 2>&1; then
  echo "+ gcloud sql users set-password postgres --instance=${SQL_INSTANCE} --password=<generated>"
  echo "+ gcloud secrets create crn-uat-postgres-password --replication-policy=automatic --data-file=-"
  if [ -z "${DRY_RUN:-}" ]; then
    pg_password="$(openssl rand -hex 24)"
    gcloud sql users set-password postgres --instance="${SQL_INSTANCE}" --password="${pg_password}"
    printf '%s' "${pg_password}" | gcloud secrets create crn-uat-postgres-password --replication-policy=automatic --data-file=-
  fi
fi

echo "+ gcloud secrets create crn-deploy-fb-token --replication-policy=automatic --data-file=-"
if [ -z "${DRY_RUN:-}" ] && ! gcloud secrets describe crn-deploy-fb-token >/dev/null 2>&1; then
  fb_token=""
  if [ -t 0 ]; then read -r -s -p "FITT Builder CODE_RUNNER token (scripts/deploy-binding.mjs create; Enter to skip): " fb_token; echo; fi
  if [ -n "${fb_token}" ]; then
    printf '%s' "${fb_token}" | gcloud secrets create crn-deploy-fb-token --replication-policy=automatic --data-file=-
  else
    echo "  skipped: create it later with the same command, value on stdin. Until then nothing deploys."
  fi
fi

common_subs="_REGION=${REGION},_SQL_INSTANCE=${PROJECT_ID}:${REGION}:${SQL_INSTANCE},_STATUS_URL=${STATUS_URL},_FB_URL=${FB_URL},_REPO=${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO}"
keep gcloud builds triggers create pubsub --name=crn-uat-deploy \
  --topic="projects/${PROJECT_ID}/topics/gcr" \
  --inline-config="${here}/cloudbuild-uat.yaml" \
  --service-account="projects/${PROJECT_ID}/serviceAccounts/${deployer}" \
  --subscription-filter='_ACTION == "INSERT"' \
  --substitutions="_ACTION=\$(body.message.data.action),_DIGEST=\$(body.message.data.digest),_TAG=\$(body.message.data.tag),${common_subs}"
keep gcloud builds triggers create pubsub --name=crn-uat-sweep \
  --topic="projects/${PROJECT_ID}/topics/crn-sweep" \
  --inline-config="${here}/cloudbuild-uat.yaml" \
  --service-account="projects/${PROJECT_ID}/serviceAccounts/${deployer}" \
  --substitutions="_ACTION=SWEEP,${common_subs}"
keep gcloud scheduler jobs create pubsub crn-sweep --location="${REGION}" --schedule="*/5 * * * *" \
  --topic=crn-sweep --message-body='{}'

echo
echo "Done. Next: issue crn-pusher's key and log docker in at the build machine (README.md,"
echo "\"Build machine\"), set CRN_MIRROR_REGISTRY=${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO} there,"
echo "and in FITT Builder set FITT_DEPLOY_SECRET_KEY. Projects then register themselves."
