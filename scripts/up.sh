#!/usr/bin/env bash
# Install Substrate and deploy the email demo on an existing GKE cluster
# (scripts/setup.sh creates the cluster and other one-time resources first).
# Idempotent: safe to re-run. Uses a private kubeconfig and registry auth
# file under .cache/, never ~/.kube/config or ~/.docker/config.json.
#
#   scripts/up.sh              install Substrate + deploy the demo
#   SKIP_SUBSTRATE=1 scripts/up.sh   redeploy only the demo pieces
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "${ROOT}/demo.env"
source "${ROOT}/scripts/lib.sh"

need gcloud kubectl podman envsubst go jq

rm -f "${KUBECONFIG}"; ensure_kubeconfig
CTX="$(kubectl config current-context)"

step "Registry auth (short-lived token, private auth file)"
mkdir -p "${DOCKER_CONFIG}"; chmod 700 "${DOCKER_CONFIG}"
gcloud auth print-access-token | podman login -u oauth2accesstoken --password-stdin "${REGION}-docker.pkg.dev" >/dev/null

FORCE=1 ensure_kubectl_ate

if [[ "${SKIP_SUBSTRATE:-0}" != 1 ]]; then
  step "Substrate: ate-setup deploy ate-system"
  (
    cd "${SUBSTRATE_DIR}"
    KO_DOCKER_REPO="${IMAGE_REPO}/substrate" KO_DEFAULTPLATFORMS=linux/amd64 \
    PROJECT_ID="${PROJECT_ID}" CLUSTER_NAME="${CLUSTER_NAME}" CLUSTER_LOCATION="${CLUSTER_LOCATION}" \
    SNAPSHOT_LOCATION="${SNAPSHOT_LOCATION}" SNAPSHOT_TRANSFER_GSA_NAME="${SNAPSHOT_TRANSFER_GSA_NAME}" \
    go run ./cmd/ate-setup --context "${CTX}" \
      --credential-provider='[{"name":"k8s.io"},{"name":"authmanager.demo","address":"authmanager-provider.ate-system.svc:50051"}]' \
      --sandbox-classes=gvisor \
      --ingress-allowed-spiffe-ids="spiffe://cluster.local/ns/ate-system/sa/atenet-client,spiffe://cluster.local/ns/email-app/sa/email-app" \
      deploy ate-system
  )
fi

step "Images"
build_push agent
build_push email-app
build_push authmanager-provider
AGENT_IMAGE="$(cat "${CACHE}/agent.ref")"
EMAIL_APP_IMAGE="$(cat "${CACHE}/email-app.ref")"
PROVIDER_IMAGE="$(cat "${CACHE}/authmanager-provider.ref")"
export AGENT_IMAGE EMAIL_APP_IMAGE PROVIDER_IMAGE

step "Worker image for this Substrate build"
WORKER_IMAGE="$(cd "${SUBSTRATE_DIR}" && KO_DOCKER_REPO="${IMAGE_REPO}/substrate" KO_DEFAULTPLATFORMS=linux/amd64 \
  go run ./cmd/ate-setup --context "${CTX}" publish worker-images | awk '/ateworker-gvisor:/{print $2}')"
[[ -n "${WORKER_IMAGE}" ]] || die "no ateworker-gvisor image ref"
export WORKER_IMAGE

step "Auth Manager credential provider (ate-system)"
envsubst '${PROVIDER_IMAGE} ${AUTH_PROVIDER}' < "${ROOT}/deploy/authmanager-provider.yaml" | kubectl apply -f -

step "Email app (namespace email-app, outside Substrate)"
build_push callback-proxy
CALLBACK_PROXY_IMAGE="$(cat "${CACHE}/callback-proxy.ref")"
CONTINUE_URL="$(continue_url || true)"
export CONTINUE_URL="${CONTINUE_URL:-https://pending.invalid/oauth/continue}"
render_app() { envsubst '${EMAIL_APP_IMAGE} ${AUTH_PROVIDER} ${CONTINUE_URL} ${DISPLAY_EMAIL} ${DEMO_EMAIL} ${ATESPACE}' < "${ROOT}/deploy/email-app.yaml"; }
render_app | kubectl apply -f -
step "Waiting for the internal callback load balancer IP"
for _ in $(seq 60); do ILB="$(internal_callback_ip || true)"; [[ -n "${ILB}" ]] && break; sleep 5; done
[[ -n "${ILB}" ]] || die "email-app-callback has no internal IP"

step "HTTPS front door: Cloud Run callback-proxy -> http://${ILB} (direct VPC egress)"
gcloud artifacts repositories add-iam-policy-binding "${IMAGE_REPO##*/}" --location "${REGION}" --project "${PROJECT_ID}" \
  --role roles/artifactregistry.reader --format=none \
  --member "serviceAccount:service-$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')@serverless-robot-prod.iam.gserviceaccount.com"
gcloud run deploy email-app-callback --image "${CALLBACK_PROXY_IMAGE}" --region "${REGION}" --project "${PROJECT_ID}" \
  --allow-unauthenticated --network default --subnet default --vpc-egress private-ranges-only \
  --set-env-vars "TARGET_URL=http://${ILB}" --min-instances 1 --max-instances 2 --quiet --format=none
CONTINUE_URL="$(continue_url)"; export CONTINUE_URL
render_app | kubectl apply -f -
echo "continue URL: ${CONTINUE_URL}"
gcloud agent-identity auth-providers update "${AUTH_PROVIDER##*/}" --project "${PROJECT_ID}" --location "${REGION}" \
  --three-legged-oauth-default-continue-uri="${CONTINUE_URL}" >/dev/null

step "Workload Identity: provider + app may call Auth Manager"
PN="$(gcloud projects describe "${PROJECT_ID}" --format='value(projectNumber)')"
for ksa in ate-system/sa/authmanager-provider email-app/sa/email-app; do
  gcloud beta agent-identity auth-providers add-iam-policy-binding "${AUTH_PROVIDER##*/}" \
    --project "${PROJECT_ID}" --location "${REGION}" --role roles/agentidentity.user \
    --member "principal://iam.googleapis.com/projects/${PN}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/${ksa}" >/dev/null
done

step "atelet may pull actor images (Workload Identity)"
gcloud artifacts repositories add-iam-policy-binding "${IMAGE_REPO##*/}" --location "${REGION}" --project "${PROJECT_ID}" \
  --role roles/artifactregistry.reader --format=none \
  --member "principal://iam.googleapis.com/projects/${PN}/locations/global/workloadIdentityPools/${PROJECT_ID}.svc.id.goog/subject/ns/ate-system/sa/atelet"

step "Worker pool"
envsubst '${WORKER_IMAGE}' < "${ROOT}/deploy/workerpool.yaml" | kubectl apply -f -

kubectl -n ate-system rollout status deploy/authmanager-provider --timeout=180s
kubectl -n email-app rollout status deploy/email-app --timeout=180s
step "Done. Run: demo/reset.sh then demo/demo.sh"
