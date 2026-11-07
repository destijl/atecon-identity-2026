#!/usr/bin/env bash
# Put the demo back to before Alice signed up, so it can be re-run:
#   - revoke Alice's Auth Manager grant so the consent screen shows again
#   - delete her agent, its template and her atespace (email-app recreates
#     them when she signs up)
#   - restart the credential provider (drops its token cache) and the email
#     app (drops any half-finished sign-up)
# Only touches demo-owned objects.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "${ROOT}/demo.env"
# shellcheck source=/dev/null
source "${ROOT}/scripts/lib.sh"
need kubectl gcloud
ensure_kubeconfig
ensure_kubectl_ate

step "Revoke Auth Manager grant for ${ATESPACE}"
gcloud agent-identity auth-providers revoke-authorization "${AUTH_PROVIDER##*/}" \
  --project "${PROJECT_ID}" --location "${REGION}" --user-id "${ATESPACE}" >/dev/null 2>&1 || true

step "Delete atespace ${ATESPACE} and its agent"
kubectl-ate delete actor "${ACTOR}" -a "${ATESPACE}" >/dev/null 2>&1 || true
kubectl-ate delete actor-template "${ACTOR}" -a "${ATESPACE}" >/dev/null 2>&1 || true
kubectl-ate delete atespace "${ATESPACE}" >/dev/null 2>&1 || true
if kubectl-ate get atespace "${ATESPACE}" >/dev/null 2>&1; then
  die "atespace ${ATESPACE} still exists; check: kubectl ate get actors -a ${ATESPACE}"
fi

step "Restart the credential provider and the email app"
kubectl -n ate-system rollout restart deploy/authmanager-provider >/dev/null
kubectl -n email-app rollout restart deploy/email-app >/dev/null
kubectl -n ate-system rollout status deploy/authmanager-provider --timeout=120s >/dev/null
kubectl -n email-app rollout status deploy/email-app --timeout=120s >/dev/null

step "Ready. Run demo/demo.sh"
