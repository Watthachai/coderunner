#!/usr/bin/env bash
# Takes a customer's UAT to production, after the customer accepted it. Run by a
# person in the team, logged in with their own account (gcloud auth login):
#
#   PROJECT_ID=<gcp project> ./deploy/gcp/promote.sh <FITT Builder project id> [--always-on] [--tier <tier>]
#
# Production gets the digest UAT serves right now — never a rebuild — on the
# customer's own Cloud SQL instance crn-prod-<id8>, its own runtime account,
# secrets (a new first-administrator password among them) and uploads bucket.
# FITT Builder shows the link and the password once. A FITT Billing license key
# can be typed at the prompt (Enter skips). Later builds never touch production;
# run this again to move it to what UAT serves then.
set -eEuo pipefail
: "${PROJECT_ID:?set PROJECT_ID}"
usage() { echo "usage: PROJECT_ID=<gcp project> $0 <FITT Builder project id> [--always-on] [--tier <tier>]" >&2; exit 1; }
pid_arg="${1:-}"
[[ "${pid_arg}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || usage
shift
MIN_INSTANCES=0
TIER="db-custom-1-3840"
while [ $# -gt 0 ]; do
  case "$1" in
    --always-on) MIN_INSTANCES=1 ;;
    --tier) TIER="${2:?--tier needs a value}"; shift ;;
    *) usage ;;
  esac
  shift
done

export STAGE=production MIN_INSTANCES
_REGION="${_REGION:-${REGION:-asia-southeast1}}"
_STATUS_URL="${_STATUS_URL:-https://fitt-builder.fittbsa.com/v1/service-status}"
_FB_URL="${_FB_URL:-https://fitt-builder.fittbsa.com}"
_SQL_INSTANCE="${_SQL_INSTANCE:-${PROJECT_ID}:${_REGION}:crn-uat}"
CRN_DEPLOY_LIB=1
# shellcheck source=uat-deploy.sh
. "$(cd "$(dirname "$0")" && pwd)/uat-deploy.sh"

pid="${pid_arg}"
id8="${pid:0:8}"
where=(--project "${PROJECT_ID}" --region "${_REGION}")

project="$(fb GET "/projects/${pid}")"
if [ "$(printf '%s' "${project}" | jget target)" != cloud ]; then
  echo "โปรเจกต์นี้เลือก local ใน FITT Builder: เปลี่ยนเป็น cloud และให้ UAT ขึ้นก่อน" >&2
  exit 1
fi

uat_service="$(gcloud run services list "${where[@]}" --filter="metadata.labels.crn-project=${id8} AND metadata.labels.crn-stage=uat" --format='value(metadata.name)' | head -1)"
if [ -z "${uat_service}" ]; then echo "ยังไม่มี UAT ของโปรเจกต์ ${pid}: production ใช้ digest ที่ผ่าน UAT เท่านั้น" >&2; exit 1; fi
# The revision serving UAT now, not the newest one: that may be a candidate that
# never passed its health check.
serving="$(gcloud run services describe "${uat_service}" "${where[@]}" --format=json | python3 -c 'import json, sys
print(next(t["revisionName"] for t in json.load(sys.stdin)["status"]["traffic"] if t.get("percent") == 100))')"
uat_image="$(gcloud run revisions describe "${serving}" "${where[@]}" --format='value(spec.containers[0].image)')"
if ! [[ "${uat_image}" =~ ^([^@]+)@(sha256:[a-f0-9]{64})$ ]]; then echo "UAT serves ${uat_image}, not an image by digest" >&2; exit 1; fi
image="${BASH_REMATCH[1]}" digest="${BASH_REMATCH[2]}"
names "${image}"
echo "promoting ${image}@${digest} from ${uat_service} to ${service}"

# The customer's own instance and its postgres password (kept for closing the
# database; no app account may read it).
gcloud sql instances describe "${sql_instance}" --project="${PROJECT_ID}" >/dev/null 2>&1 \
  || gcloud sql instances create "${sql_instance}" --project="${PROJECT_ID}" --database-version=POSTGRES_16 --edition=ENTERPRISE \
    --tier="${TIER}" --region="${_REGION}" --storage-type=SSD --storage-size=10 \
    --backup-start-time=19:00 --retained-backups-count=7 >/dev/null
if ! gcloud secrets describe "${postgres_secret}" --project="${PROJECT_ID}" >/dev/null 2>&1; then
  pg_password="$(random_hex 24)"
  gcloud sql users set-password postgres --instance="${sql_instance}" --project="${PROJECT_ID}" --password="${pg_password}" >/dev/null
  printf '%s' "${pg_password}" | gcloud secrets create "${postgres_secret}" --project="${PROJECT_ID}" --replication-policy=automatic --data-file=- >/dev/null
fi
if [ -t 0 ] && ! has_secret license-key; then
  license=""
  read -r -s -p "License key from FITT Billing (Enter to skip): " license; echo
  if [ -n "${license}" ]; then
    gcloud iam service-accounts describe "${runtime_email}" --project="${PROJECT_ID}" >/dev/null 2>&1 \
      || gcloud iam service-accounts create "${runtime}" --project="${PROJECT_ID}" --display-name="production app ${service}" >/dev/null
    printf '%s' "${license}" | put_secret license-key
  fi
fi

deploy_one "${image}" "${digest}"
