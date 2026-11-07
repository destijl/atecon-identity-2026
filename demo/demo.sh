#!/usr/bin/env bash
# Pleading the Agent — "scoped secret" rung. Two parts, as on slides 13-14:
#   sign-up: Alice signs up at email-app and consents to gmail.readonly; the
#            app then creates her atespace, agent and egress policy.
#   request: the agent, holding no credentials, reads her Gmail through an
#            OAuth token Substrate's egress gateway injects on the way out.
#
#   demo/demo.sh                 interactive (ENTER advances each step)
#   demo/demo.sh -n              unattended (no typing or pauses; still waits
#                                for you to click through consent)
#   TYPE_SPEED_OVERRIDE=40 demo/demo.sh   faster typing (needs pv)
#
# Run demo/reset.sh first: it deletes Alice's agent and revokes her grant.
#
# The operator talks to the email app's operator port, which signs Alice up
# and reaches her agent via Substrate's ingress router with its pod identity:
#   you -> kubectl port-forward -> email-app (:8070) -> atenet-router -> actor
# No -u: demo-magic.sh reads unset variables.
set -eo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=/dev/null
source "${ROOT}/demo.env"
# shellcheck source=/dev/null
source "${ROOT}/scripts/lib.sh"

UNATTENDED=false
[[ "${1:-}" == "-n" ]] && UNATTENDED=true

export TERM="${TERM:-xterm-256color}"
# shellcheck source=/dev/null
. "${ROOT}/third_party/demo-magic.sh"
TYPE_SPEED="${TYPE_SPEED_OVERRIDE:-30}"
command -v pv >/dev/null || unset TYPE_SPEED   # no pv: print commands instantly
DEMO_PROMPT="${GREEN}➜ ${COLOR_RESET}"
DEMO_CMD_COLOR="${WHITE}"
if $UNATTENDED; then NO_WAIT=true; unset TYPE_SPEED; fi

ensure_kubeconfig
ensure_kubectl_ate
PORT=18070
PF_PID=""
cleanup() { if [[ -n "${PF_PID}" ]]; then kill "${PF_PID}" 2>/dev/null || true; fi; }
trap cleanup EXIT
if ! curl -fsS -o /dev/null -m 2 "localhost:${PORT}/readyz" 2>/dev/null; then
  kubectl -n email-app port-forward deploy/email-app "${PORT}:8070" >"${CACHE}/pf-email-app.log" 2>&1 &
  PF_PID=$!
  ready=false
  for _ in $(seq 30); do
    if curl -fsS -o /dev/null -m 2 "localhost:${PORT}/readyz" 2>/dev/null; then
      ready=true
      break
    fi
    sleep 1
  done
  if ! $ready; then
    echo "ERROR: email-app failed to become ready on localhost:${PORT}." >&2
    if [[ -f "${CACHE}/pf-email-app.log" ]]; then
      echo "--- port-forward log (${CACHE}/pf-email-app.log) ---" >&2
      cat "${CACHE}/pf-email-app.log" >&2
    fi
    exit 1
  fi
fi
START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# On-screen helpers. "email-app signup <email>" signs a user up; "agent <op>"
# asks Alice's agent to do something, through the app.
email-app() {
  [[ "$1" == signup ]] || return 1
  local res url
  res="$(curl -sS -m 60 -X POST "localhost:${PORT}/signup?email=$2")"
  url="$(jq -r '.authorizationUrl // empty' <<<"${res}")"
  if [[ -n "${url}" ]]; then
    printf '%s\n' "${url}" > "${CACHE}/consent-url.txt"
    printf '%s\n' "${url}"
  else
    printf '%s\n' "${res}"
  fi
}
agent() {
  local method=GET; [[ "$1" == trash ]] && method=POST
  curl -sS -m 60 -X "${method}" "localhost:${PORT}/agent/$1" | sed "s/${DEMO_EMAIL}/${DISPLAY_EMAIL}/g" | jq -C .
}
banner() { echo; printf '%b\n' "${BOLD}${YELLOW}# $*${COLOR_RESET}"; }
note() { printf '%b\n' "${CYAN}#   $*${COLOR_RESET}"; }
pause() { $UNATTENDED || wait; }

# section starts a step on a clean screen, a few lines down, so the output
# stays in view. In interactive mode it first waits for ENTER so the audience
# can finish reading the previous step. TOP_PAD sets the blank lines.
TOP_PAD="${TOP_PAD:-3}"
FIRST=true
section() {
  $FIRST || pause
  FIRST=false
  clear
  for ((i = 0; i < TOP_PAD; i++)); do echo; done
  banner "$@"
}

section "SIGN-UP  1. The app's OAuth client, registered in Google Cloud Agent Auth Manager"
p "gcloud agent-identity auth-providers describe ${AUTH_PROVIDER_NAME} --location ${REGION}"
gcloud agent-identity auth-providers describe "${AUTH_PROVIDER_NAME}" --project "${PROJECT_ID}" --location "${REGION}" \
  --format=json 2>/dev/null \
  | jq -C '{allowedScopes, redirectUrl: .authProviderTypeParams.threeLeggedOauth.redirectUrl, continueUri: .authProviderTypeParams.threeLeggedOauth.defaultContinueUri}'
echo
note "allowedScopes: the most this client can ever be granted (read-only Gmail)"
note "redirectUrl: Google returns the code to Auth Manager, which stores the tokens"
note "continueUri: Auth Manager then sends Alice's browser back to the email app"

section "SIGN-UP  2. Alice signs up at the email app to grant read-only access (gmail.readonly)"
pe "email-app signup ${DISPLAY_EMAIL}"
echo
printf '%s' "   waiting for consent and for the app to create Alice's agent "
# Watch the app's log; signing up again would start a new consent attempt.
until kubectl -n email-app logs deploy/email-app --since-time="${START}" 2>/dev/null | grep -q '"actor created"'; do
  if kubectl -n email-app logs deploy/email-app --since-time="${START}" 2>/dev/null | grep -q '"creating agent failed"'; then
    echo; echo "   the app could not create the agent:"; kubectl -n email-app logs deploy/email-app --since-time="${START}" | grep '"creating agent failed"' | tail -1
    exit 1
  fi
  printf '.'; sleep 2
done
echo " ✓"

section "SIGN-UP  3. The app created Alice's atespace, agent and egress policy through the Substrate API"
pe "kubectl ate get actors -a ${ATESPACE}"
p "kubectl ate get egress-policy ${ACTOR} -a ${ATESPACE} -o yaml"
kubectl ate get egress-policy "${ACTOR}" -a "${ATESPACE}" -o yaml 2>/dev/null \
  | awk '/^metadata:/{skip=1; next} /^[^ ]/{skip=0} !skip'
echo

section "REQUEST  4. The agent searches Alice's Gmail for subject \"Agent Substrate Day:\""
pe "agent search"
note "The agent sent a placeholder header; the gateway added the token on the way out."

section "REQUEST  5. The agent tries to trash a message"
pe "agent trash"
note "Google refuses: the token is scoped to gmail.readonly"

section "REQUEST  6. What the logs say"
p "kubectl logs -n ate-system deploy/atenet-egress -c envoy | grep gmail"
# One line per Gmail call the agent made; the per-message metadata reads
# are left out to keep it short (and keep message IDs off screen).
kubectl -n ate-system logs deploy/atenet-egress -c envoy --since-time="${START}" 2>/dev/null \
  | grep '"leg":"mitm"' | grep gmail.googleapis.com \
  | jq -C -c 'select(.path | test("/messages/[^/]+$") | not) | {actor, method, path: (.path|sub("\\?.*";"")), status}' || true
echo
p "kubectl logs -n ate-system deploy/authmanager-provider | grep injected"
kubectl -n ate-system logs deploy/authmanager-provider --since-time="${START}" 2>/dev/null \
  | grep 'credential injected' | tail -1 | jq -C -c '{msg, actor, user, scope}' || true
pause
