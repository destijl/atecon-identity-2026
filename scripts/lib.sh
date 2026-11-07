# Shared helpers. Source after demo.env.
# shellcheck shell=bash

CACHE="${ROOT}/.cache"
mkdir -p "${CACHE}"
export KUBECONFIG="${CACHE}/kubeconfig"
export DOCKER_CONFIG="${CACHE}/docker"
export REGISTRY_AUTH_FILE="${DOCKER_CONFIG}/config.json"
export PATH="${CACHE}/bin:${PATH}"

ATESPACE="$(printf '%s' "${DISPLAY_EMAIL}" | sed -E 's/[@._+]/-/g; s/-+/-/g; s/^-|-$//g')"
export ATESPACE
export ACTOR="email-agent"

step() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
die() { printf '\033[1;31mERROR: %s\033[0m\n' "$*" >&2; exit 1; }
need() { for t in "$@"; do command -v "$t" >/dev/null || die "'$t' is required"; done; }

# build_push <cmd>: build cmd/<cmd> in a container and push; writes the
# digest-pinned ref to .cache/<cmd>.ref.
build_push() {
  local name="$1" tag
  tag="${IMAGE_REPO}/${name}:$(git -C "${ROOT}" rev-parse --short HEAD)-$(date +%s)"
  podman build -q -f "${ROOT}/deploy/Containerfile" --build-arg "CMD=${name}" -t "${tag}" "${ROOT}" >/dev/null
  podman push -q --digestfile "${CACHE}/${name}.digest" "${tag}" >/dev/null
  echo "${IMAGE_REPO}/${name}@$(cat "${CACHE}/${name}.digest")" > "${CACHE}/${name}.ref"
  echo "  ${name}: $(cat "${CACHE}/${name}.ref")"
}

# internal_callback_ip prints the email app callback's internal LB IP.
internal_callback_ip() {
  kubectl -n email-app get svc email-app-callback -o jsonpath='{.status.loadBalancer.ingress[0].ip}' 2>/dev/null
}

# continue_url prints the public HTTPS callback (Cloud Run front door).
continue_url() {
  local u
  u="$(gcloud run services describe email-app-callback --region "${REGION}" --project "${PROJECT_ID}" --format='value(status.url)' 2>/dev/null)"
  [[ -n "${u}" ]] && echo "${u}/oauth/continue"
}

# ensure_kubeconfig writes cluster credentials to the private kubeconfig
# (never ~/.kube/config) if it isn't there yet.
ensure_kubeconfig() {
  [[ -s "${KUBECONFIG}" ]] && return 0
  step "Cluster credentials -> ${KUBECONFIG}"
  gcloud container clusters get-credentials "${CLUSTER_NAME}" \
    --location "${CLUSTER_LOCATION}" --project "${PROJECT_ID}" >/dev/null
}

# ensure_kubectl_ate builds kubectl-ate from Substrate at SUBSTRATE_COMMIT
# into .cache/bin. FORCE=1 rebuilds even if it already exists.
ensure_kubectl_ate() {
  [[ "${FORCE:-0}" != 1 && -x "${CACHE}/bin/kubectl-ate" ]] && return 0
  need git go
  if [[ -z "${SUBSTRATE_DIR:-}" || ! -e "${SUBSTRATE_DIR}/.git" ]]; then
    SUBSTRATE_DIR="${CACHE}/substrate"
    [[ -e "${SUBSTRATE_DIR}/.git" ]] || git clone -q "${SUBSTRATE_REPO:-https://github.com/agent-substrate/substrate}" "${SUBSTRATE_DIR}"
  fi
  git -C "${SUBSTRATE_DIR}" -c advice.detachedHead=false checkout -q "${SUBSTRATE_COMMIT}" 2>/dev/null \
    || { git -C "${SUBSTRATE_DIR}" fetch -q origin && git -C "${SUBSTRATE_DIR}" -c advice.detachedHead=false checkout -q "${SUBSTRATE_COMMIT}"; }
  [[ "$(git -C "${SUBSTRATE_DIR}" rev-parse HEAD)" == "${SUBSTRATE_COMMIT}"* ]] \
    || die "${SUBSTRATE_DIR} is not at ${SUBSTRATE_COMMIT}"
  step "Build kubectl-ate (${SUBSTRATE_COMMIT})"
  (cd "${SUBSTRATE_DIR}" && go build -o "${CACHE}/bin/kubectl-ate" ./cmd/kubectl-ate)
}

# agent_ref prints the digest-pinned agent image: the one this machine last
# built (.cache/agent.ref), else the newest one in Artifact Registry (for
# machines that only run the demo, e.g. a laptop).
agent_ref() {
  local ref=""
  [[ -s "${CACHE}/agent.ref" ]] && ref="$(cat "${CACHE}/agent.ref")"
  [[ -n "${ref}" ]] || ref="$(gcloud artifacts docker images list "${IMAGE_REPO}/agent" \
    --sort-by=~UPDATE_TIME --limit=1 --format='value(format("{0}@{1}",package,version))' 2>/dev/null)"
  [[ -n "${ref}" ]] || die "no agent image: run scripts/up.sh first"
  printf '%s\n' "${ref}"
}
