#!/usr/bin/env bash
# Put the demo back to its starting state so it can be re-run:
#   - recreate the user's atespace, the agent actor and its egress policy
#   - revoke the user's Auth Manager grant so the consent screen shows again
#   - restart the credential provider (drops its token cache)
# Only touches demo-owned objects (the user's atespace and its actor).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "${ROOT}/demo.env"
# shellcheck source=/dev/null
source "${ROOT}/scripts/lib.sh"
need kubectl-ate kubectl gcloud envsubst

step "Revoke Auth Manager grant for ${ATESPACE}"
gcloud agent-identity auth-providers revoke-authorization "${AUTH_PROVIDER##*/}" \
  --project "${PROJECT_ID}" --location "${REGION}" --user-id "${ATESPACE}" >/dev/null 2>&1 || true

step "Recreate atespace ${ATESPACE} and actor ${ACTOR}"
kubectl-ate delete actor "${ACTOR}" -a "${ATESPACE}" >/dev/null 2>&1 || true
kubectl-ate create atespace "${ATESPACE}" >/dev/null 2>&1 || true
kubectl-ate delete actor-template email-agent -a "${ATESPACE}" >/dev/null 2>&1 || true
AGENT_IMAGE="$(cat "${CACHE}/agent.ref")" ATESPACE="${ATESPACE}" \
  envsubst '${AGENT_IMAGE} ${ATESPACE}' < "${ROOT}/deploy/actor-template.yaml.tmpl" > "${CACHE}/actor-template.yaml"
kubectl-ate create actor-template -f "${CACHE}/actor-template.yaml"
kubectl-ate create actor "${ACTOR}" -a "${ATESPACE}" --template email-agent
kubectl-ate create egress-policy "${ACTOR}" -a "${ATESPACE}" -f "${ROOT}/deploy/egress-policy.yaml"

step "Restart the credential provider (clears its token cache)"
kubectl -n ate-system rollout restart deploy/authmanager-provider >/dev/null
kubectl -n ate-system rollout status deploy/authmanager-provider --timeout=120s >/dev/null

step "Wake the actor"
kubectl-ate resume actor "${ACTOR}" -a "${ATESPACE}" >/dev/null 2>&1 || true
step "Ready. Run demo/demo.sh"
