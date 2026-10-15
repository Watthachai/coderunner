#!/usr/bin/env bash
# One-time GCP setup for Code Runner's UAT path (see README.md in this folder).
# Run by someone allowed to administer PROJECT_ID, logged in with their own account
# (gcloud auth login). Safe to re-run: what already exists is kept.
#
#   PROJECT_ID=<project> ./deploy/gcp/setup.sh          # DRY_RUN=1 prints, runs nothing
#
# Creates in PROJECT_ID / REGION:
#   - Artifact Registry Docker repository REPO   (CRN_MIRROR_REGISTRY points here)
#   - Pub/Sub topic "gcr"                         (Artifact Registry announces images on it)
#   - service accounts, each with only what it needs:
#       crn-pusher        writes images to REPO (used by the Code Runner build machine)
#       crn-uat-deployer  runs the trigger: deploys UAT services as crn-uat-runtime
#       crn-uat-runtime   what UAT apps run as: Cloud SQL client; secrets granted per project
#   - Cloud SQL for PostgreSQL 16 instance SQL_INSTANCE, shared by UAT projects (one
#     database each). It runs all month — see the cost estimate before creating it.
#   - the Cloud Build Pub/Sub trigger crn-uat-deploy running cloudbuild-uat.yaml
#
# It creates no key for crn-pusher: issuing a credential is a separate, deliberate
# step (README.md, "Build machine").
set -euo pipefail
: "${PROJECT_ID:?set PROJECT_ID}"
REGION="${REGION:-asia-southeast3}"
REPO="${REPO:-crn}"
SQL_INSTANCE="${SQL_INSTANCE:-crn-uat}"
SQL_TIER="${SQL_TIER:-db-custom-1-3840}"
STATUS_URL="${STATUS_URL:-https://fitt-builder.fittbsa.com/v1/service-status}"
here="$(cd "$(dirname "$0")" && pwd)"

run() { echo "+ $*"; if [ -z "${DRY_RUN:-}" ]; then "$@"; fi; }
# create-if-missing: a resource that already exists is not an error on a re-run.
keep() { run "$@" || echo "  (kept as it is: it already exists)"; }
sa() { echo "$1@${PROJECT_ID}.iam.gserviceaccount.com"; }

run gcloud config set project "${PROJECT_ID}"
run gcloud services enable artifactregistry.googleapis.com run.googleapis.com sqladmin.googleapis.com \
  secretmanager.googleapis.com cloudbuild.googleapis.com pubsub.googleapis.com iam.googleapis.com
keep gcloud artifacts repositories create "${REPO}" --repository-format=docker --location="${REGION}" \
  --description="Code Runner images, mirrored with their digests unchanged"
keep gcloud pubsub topics create gcr

keep gcloud iam service-accounts create crn-pusher --display-name="Code Runner image pusher"
keep gcloud iam service-accounts create crn-uat-deployer --display-name="Code Runner UAT deployer (Cloud Build)"
keep gcloud iam service-accounts create crn-uat-runtime --display-name="Code Runner UAT apps"

run gcloud artifacts repositories add-iam-policy-binding "${REPO}" --location="${REGION}" \
  --member="serviceAccount:$(sa crn-pusher)" --role=roles/artifactregistry.writer
run gcloud artifacts repositories add-iam-policy-binding "${REPO}" --location="${REGION}" \
  --member="serviceAccount:$(sa crn-uat-deployer)" --role=roles/artifactregistry.reader
run gcloud artifacts repositories add-iam-policy-binding "${REPO}" --location="${REGION}" \
  --member="serviceAccount:$(sa crn-uat-runtime)" --role=roles/artifactregistry.reader
run gcloud projects add-iam-policy-binding "${PROJECT_ID}" --condition=None \
  --member="serviceAccount:$(sa crn-uat-runtime)" --role=roles/cloudsql.client
for role in roles/run.admin roles/secretmanager.viewer roles/logging.logWriter; do
  run gcloud projects add-iam-policy-binding "${PROJECT_ID}" --condition=None \
    --member="serviceAccount:$(sa crn-uat-deployer)" --role="${role}"
done
run gcloud iam service-accounts add-iam-policy-binding "$(sa crn-uat-runtime)" \
  --member="serviceAccount:$(sa crn-uat-deployer)" --role=roles/iam.serviceAccountUser

keep gcloud sql instances create "${SQL_INSTANCE}" --database-version=POSTGRES_16 --edition=ENTERPRISE \
  --tier="${SQL_TIER}" --region="${REGION}" --storage-type=SSD --storage-size=10 \
  --backup-start-time=19:00 --retained-backups-count=7

keep gcloud builds triggers create pubsub --name=crn-uat-deploy \
  --topic="projects/${PROJECT_ID}/topics/gcr" \
  --inline-config="${here}/cloudbuild-uat.yaml" \
  --service-account="projects/${PROJECT_ID}/serviceAccounts/$(sa crn-uat-deployer)" \
  --subscription-filter='_ACTION == "INSERT"' \
  --substitutions="_ACTION=\$(body.message.data.action),_DIGEST=\$(body.message.data.digest),_TAG=\$(body.message.data.tag),_REGION=${REGION},_SQL_INSTANCE=${PROJECT_ID}:${REGION}:${SQL_INSTANCE},_STATUS_URL=${STATUS_URL}"

echo
echo "Done. Next: give the build machine crn-pusher's credential (README.md, \"Build machine\"),"
echo "set CRN_MIRROR_REGISTRY=${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO}, then enrol each project with new-uat.sh."
