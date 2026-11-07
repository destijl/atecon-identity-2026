# Shared helpers. Source after demo.env.
# shellcheck shell=bash

CACHE="${ROOT}/.cache"
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
