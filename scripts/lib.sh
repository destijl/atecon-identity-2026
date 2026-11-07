# Shared helpers. Source after demo.env.
# shellcheck shell=bash

CACHE="${ROOT}/.cache"
mkdir -p "${CACHE}"
export KUBECONFIG="${CACHE}/kubeconfig"
export DOCKER_CONFIG="${CACHE}/docker"
export REGISTRY_AUTH_FILE="${DOCKER_CONFIG}/config.json"
export PATH="${CACHE}/bin:${PATH}"

# One atespace per user: "u-" + the email's local part (alice@gmail.com ->
# u-alice), the same rule as internal/identity. email-app creates it at sign-up.
ATESPACE="u-$(printf '%s' "${DISPLAY_EMAIL%%@*}" | sed -E 's/[._+]/-/g; s/-+/-/g; s/-$//')"
export ATESPACE
export ACTOR="mail-agent"

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
# into .cache/bin. A wrapper passes a minted ate-client token to silence the
# fallback warning on-screen while keeping typed commands clean.
ensure_kubectl_ate() {
  if [[ -f "${CACHE}/bin/kubectl-ate" && ! -f "${CACHE}/bin/kubectl-ate.bin" ]]; then
    # Migrate any existing binary to .bin so the wrapper takes its place.
    mv "${CACHE}/bin/kubectl-ate" "${CACHE}/bin/kubectl-ate.bin"
  fi
  write_kubectl_ate_wrapper() {
    cat > "${CACHE}/bin/kubectl-ate" <<'EOF'
#!/usr/bin/env bash
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${DIR}/kubectl-ate.bin"
TOKEN_FILE="${DIR}/../ate-client.token"
if [[ ! -s "${TOKEN_FILE}" ]] && command -v kubectl >/dev/null 2>&1; then
  kubectl create token -n ate-system ate-client --audience api.ate-system.svc --duration=24h > "${TOKEN_FILE}" 2>/dev/null || true
fi
if [[ -s "${TOKEN_FILE}" ]] && ! [[ " $* " =~ [[:space:]]--token-file[[:space:]=] ]]; then
  exec "${BIN}" --token-file "${TOKEN_FILE}" "$@"
fi
exec "${BIN}" "$@"
EOF
    chmod +x "${CACHE}/bin/kubectl-ate"
  }
  if [[ "${FORCE:-0}" != 1 && -x "${CACHE}/bin/kubectl-ate.bin" ]]; then
    [[ -x "${CACHE}/bin/kubectl-ate" ]] || write_kubectl_ate_wrapper
    return 0
  fi
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
  (cd "${SUBSTRATE_DIR}" && go build -o "${CACHE}/bin/kubectl-ate.bin" ./cmd/kubectl-ate)
  write_kubectl_ate_wrapper
}

# log_all_egress makes Substrate's egress gateway log every actor request.
# Its access log filter keeps status >= 400 (plus a 1% sample) by default; the
# runtime key meant to lower that (access_log.min_status) can't be set because
# the gateway's Envoy has no admin runtime layer. So lower the default itself.
log_all_egress() {
  local cfg
  cfg="$(kubectl -n ate-system get configmap atenet-egress -o json)"
  if ! grep -q 'default_value: 400, runtime_key: access_log.min_status' <<<"${cfg}"; then
    return 0   # already done, or the manifest changed shape
  fi
  jq '.data |= with_entries(.value |= gsub("default_value: 400, runtime_key: access_log.min_status"; "default_value: 0, runtime_key: access_log.min_status"))' \
    <<<"${cfg}" | kubectl replace -f - >/dev/null
  kubectl -n ate-system rollout restart deploy/atenet-egress >/dev/null
  kubectl -n ate-system rollout status deploy/atenet-egress --timeout=180s >/dev/null
}
