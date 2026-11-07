#!/usr/bin/env bash
# Create the demo environment from scratch in a Google Cloud project, then
# deploy the demo. Idempotent: every step skips what already exists.
#
#   cp demo.env.example demo.env && $EDITOR demo.env
#   scripts/setup.sh
#
# What it creates (all named from demo.env):
#   - APIs: GKE, Artifact Registry, Cloud Run, IAM, STS, Storage, Agent Identity
#   - GKE Standard cluster with Workload Identity and Dataplane V2
#   - Artifact Registry Docker repo
#   - GCS snapshot bucket + snapshot-transfer service account (for Substrate)
#   - Agent Auth Manager 3LO auth provider (asks for your OAuth client)
# Then runs scripts/up.sh (Substrate install + demo deploy).
#
# One manual step: the OAuth client lives in a project whose consent screen
# can be External (personal projects can; many org projects can't). The
# script prints exactly what to click, then reads the client ID and secret
# from your terminal (the secret is never echoed or written to disk).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ -f "${ROOT}/demo.env" ]] || { echo "Create demo.env first: cp demo.env.example demo.env" >&2; exit 1; }
# shellcheck source=/dev/null
source "${ROOT}/demo.env"
# shellcheck source=/dev/null
source "${ROOT}/scripts/lib.sh"

need gcloud kubectl podman envsubst go jq git
for v in PROJECT_ID REGION CLUSTER_NAME CLUSTER_LOCATION GKE_VERSION IMAGE_REPO_NAME AUTH_PROVIDER_NAME \
         SNAPSHOT_BUCKET SNAPSHOT_TRANSFER_GSA_NAME DEMO_EMAIL DISPLAY_EMAIL; do
  [[ -n "${!v:-}" && "${!v}" != your-* ]] || die "set ${v} in demo.env"
done
gc() { gcloud --project "${PROJECT_ID}" "$@"; }
PN="$(gc projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
POOL="${PROJECT_ID}.svc.id.goog"
ksa() { echo "principal://iam.googleapis.com/projects/${PN}/locations/global/workloadIdentityPools/${POOL}/subject/ns/$1/sa/$2"; }

step "Enable APIs"
gc services enable container.googleapis.com artifactregistry.googleapis.com run.googleapis.com \
  iam.googleapis.com iamcredentials.googleapis.com sts.googleapis.com storage.googleapis.com \
  compute.googleapis.com agentidentity.googleapis.com agentidentitycredentials.googleapis.com

step "GKE cluster ${CLUSTER_NAME} (${CLUSTER_LOCATION}, ${GKE_VERSION})"
if gc container clusters describe "${CLUSTER_NAME}" --location "${CLUSTER_LOCATION}" >/dev/null 2>&1; then
  echo "  exists"
else
  gc container clusters create "${CLUSTER_NAME}" --location "${CLUSTER_LOCATION}" \
    --release-channel regular --cluster-version "${GKE_VERSION}" \
    --machine-type "${NODE_MACHINE_TYPE:-c3-standard-4}" --num-nodes "${NODE_COUNT:-2}" \
    --workload-pool "${POOL}" --workload-metadata GKE_METADATA \
    --enable-dataplane-v2 --enable-shielded-nodes --shielded-secure-boot
fi

step "Artifact Registry repo ${IMAGE_REPO_NAME}"
gc artifacts repositories describe "${IMAGE_REPO_NAME}" --location "${REGION}" >/dev/null 2>&1 \
  || gc artifacts repositories create "${IMAGE_REPO_NAME}" --repository-format docker --location "${REGION}" \
       --description "Substrate OAuth demo images"

step "Snapshot bucket gs://${SNAPSHOT_BUCKET} and ${SNAPSHOT_TRANSFER_GSA_NAME} service account"
gcloud storage buckets describe "gs://${SNAPSHOT_BUCKET}" --project "${PROJECT_ID}" >/dev/null 2>&1 \
  || gcloud storage buckets create "gs://${SNAPSHOT_BUCKET}" --project "${PROJECT_ID}" --location "${REGION}" \
       --uniform-bucket-level-access --public-access-prevention
GSA="${SNAPSHOT_TRANSFER_GSA_NAME}@${PROJECT_ID}.iam.gserviceaccount.com"
gc iam service-accounts describe "${GSA}" >/dev/null 2>&1 \
  || gc iam service-accounts create "${SNAPSHOT_TRANSFER_GSA_NAME}" --display-name "Substrate snapshot transfer"
# ate-api-server's credential exchange impersonates the GSA for snapshot I/O.
gc iam service-accounts add-iam-policy-binding "${GSA}" --role roles/iam.serviceAccountTokenCreator \
  --member "$(ksa ate-system ate-api-server)" --format=none
for role in roles/storage.objectAdmin roles/storage.bucketViewer; do
  for m in "serviceAccount:${GSA}" "$(ksa ate-system ate-api-server)" "$(ksa ate-system atelet)"; do
    gcloud storage buckets add-iam-policy-binding "gs://${SNAPSHOT_BUCKET}" --role "${role}" --member "${m}" --format=none
  done
done

step "Agent Auth Manager provider ${AUTH_PROVIDER_NAME}"
if gc agent-identity auth-providers describe "${AUTH_PROVIDER_NAME}" --location "${REGION}" >/dev/null 2>&1; then
  echo "  exists"
else
  cat <<EOF

  Create the OAuth client (Google Cloud console, a project that allows an
  External consent screen):
    1. APIs & Services > Library > Gmail API > Enable
    2. Google Auth Platform > Audience: External, Testing; add test user ${DEMO_EMAIL}
    3. Data Access > Add scope https://www.googleapis.com/auth/gmail.readonly
    4. Clients > Create client > Web application, Authorized redirect URI:
       https://agentidentitycredentials.googleapis.com/v1/${AUTH_PROVIDER}/oauthcallback

EOF
  read -r -p "  OAuth client ID: " CID
  read -r -s -p "  OAuth client secret (hidden): " CSEC; echo
  [[ -n "${CID}" && -n "${CSEC}" ]] || die "client ID and secret are required"
  gc agent-identity auth-providers create "${AUTH_PROVIDER_NAME}" --location "${REGION}" \
    --three-legged-oauth-client-id="${CID}" --three-legged-oauth-client-secret="${CSEC}" \
    --three-legged-oauth-authorization-url=https://accounts.google.com/o/oauth2/v2/auth \
    --three-legged-oauth-token-url=https://oauth2.googleapis.com/token \
    --three-legged-oauth-default-continue-uri=https://pending.invalid/oauth/continue \
    --allowed-scopes=https://www.googleapis.com/auth/gmail.readonly --format=none
  unset CID CSEC
fi

step "Install Substrate and deploy the demo"
"${ROOT}/scripts/up.sh"
cat <<EOF

Environment ready. Run:
  demo/reset.sh   # delete Alice's agent and atespace, revoke her grant
  demo/demo.sh    # or -n for unattended
EOF
