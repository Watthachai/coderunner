#!/usr/bin/env bash
# Enrols one Code Runner project in UAT on GCP: a database and user on the shared
# UAT instance, and the secrets its Cloud Run service reads. After this, the next
# image of the project that reaches Artifact Registry deploys itself (uat-deploy.sh).
#
#   PROJECT_ID=<project> BOOTSTRAP_ADMIN_EMAIL=<first admin> ./deploy/gcp/new-uat.sh crn-demo-<slug>-<id8>
#
# Secret values go to Secret Manager on stdin, never as arguments. The first
# administrator's password is generated here and shown once, to the person running
# this, to hand over through the agreed secret channel. A license key, if the
# project already has one from FITT Billing, is typed at the prompt (Enter skips).
set -euo pipefail
: "${PROJECT_ID:?set PROJECT_ID}"
: "${BOOTSTRAP_ADMIN_EMAIL:?set BOOTSTRAP_ADMIN_EMAIL (the first administrator)}"
image_name="${1:?usage: new-uat.sh crn-demo-<slug>-<id8>}"
REGION="${REGION:-asia-southeast3}"
REPO="${REPO:-crn}"
SQL_INSTANCE="${SQL_INSTANCE:-crn-uat}"
here="$(cd "$(dirname "$0")" && pwd)"

service="$(PRINT_SERVICE=1 _ACTION=INSERT _TAG=enrol \
  _DIGEST="${REGION}-docker.pkg.dev/${PROJECT_ID}/${REPO}/${image_name}@sha256:$(printf '0%.0s' {1..64})" \
  bash "${here}/uat-deploy.sh")"
db="$(printf '%s' "${service}" | tr '-' '_')"
runtime="crn-uat-runtime@${PROJECT_ID}.iam.gserviceaccount.com"
connection="${PROJECT_ID}:${REGION}:${SQL_INSTANCE}"
echo "enrolling ${image_name} as ${service} (database ${db} on ${connection})"

random() { openssl rand -hex "$1"; }
put_secret() { # name, value on stdin
  gcloud secrets create "${service}-$1" --project="${PROJECT_ID}" --replication-policy=automatic --data-file=- >/dev/null
  gcloud secrets add-iam-policy-binding "${service}-$1" --project="${PROJECT_ID}" \
    --member="serviceAccount:${runtime}" --role=roles/secretmanager.secretAccessor >/dev/null
}

db_password="$(random 24)"
gcloud sql databases create "${db}" --instance="${SQL_INSTANCE}" --project="${PROJECT_ID}"
# gcloud takes a new user's password only as a flag; it is random, used once, and
# then lives only in Secret Manager.
gcloud sql users create "${db}" --instance="${SQL_INSTANCE}" --project="${PROJECT_ID}" --password="${db_password}"
printf 'postgresql://%s:%s@localhost/%s?host=/cloudsql/%s' "${db}" "${db_password}" "${db}" "${connection}" | put_secret database-url
random 32 | put_secret auth-secret
printf '%s' "${BOOTSTRAP_ADMIN_EMAIL}" | put_secret bootstrap-admin-email
admin_password="$(random 12)"
printf '%s' "${admin_password}" | put_secret bootstrap-admin-password
license=""
if [ -t 0 ]; then read -r -s -p "License key from FITT Billing (Enter to skip): " license; echo; fi
if [ -n "${license}" ]; then printf '%s' "${license}" | put_secret license-key; fi

echo
echo "Enrolled ${service}. Its next image in Artifact Registry deploys to UAT by itself."
echo "First administrator: ${BOOTSTRAP_ADMIN_EMAIL}"
echo "Initial password (shown once, hand it over through the secret channel): ${admin_password}"
