#!/usr/bin/env bash
# Pleading the Agent — "scoped secret" rung: an agent with zero credentials
# reads Alice's Gmail through an OAuth token that Substrate's egress gateway
# injects on the way out.
#
#   demo/demo.sh                 interactive (ENTER advances each step)
#   demo/demo.sh -n              unattended (no typing or pauses; still waits
#                                for you to click through consent)
#   TYPE_SPEED_OVERRIDE=40 demo/demo.sh   faster typing (needs pv)
#
# Run demo/reset.sh first so the consent screen shows again.
#
# The operator talks to the agent through the email app, which reaches it via
# Substrate's ingress router with the app's own pod identity:
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
DEMO_PROMPT="${GREEN}➜ ${CYAN}email-app ${COLOR_RESET}"
DEMO_CMD_COLOR="${WHITE}"
if $UNATTENDED; then NO_WAIT=true; unset TYPE_SPEED; fi

ensure_kubeconfig
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

# On-screen helper: "agent <op>" asks Alice's agent to do something.
agent() {
  local method=GET; [[ "$1" == trash ]] && method=POST
  curl -sS -m 60 -X "${method}" "localhost:${PORT}/agent/$1" | sed "s/${DEMO_EMAIL}/${DISPLAY_EMAIL}/g" | jq -C .
}
banner() { echo; printf '%b\n' "${BOLD}${YELLOW}# $*${COLOR_RESET}"; }
pause() { $UNATTENDED || wait; }

clear
banner "Alice's email agent runs in a Substrate sandbox (atespace ${ATESPACE})."
banner "1. What credentials does the agent hold?"
pe "agent audit"

banner "2. Search Alice's Gmail for subject \"Agent Substrate Day:\""
pe "agent search"
banner "   403 from Substrate's egress gateway: no grant for Alice yet. Ask the app for a consent link."
p "agent consent"
CONSENT="$(curl -sS -m 60 "localhost:${PORT}/agent/consent")"
jq -C '.authorizationUrl |= (if . then (.[0:60] + "…") else . end)' <<<"${CONSENT}"
URL="$(jq -r '.authorizationUrl // empty' <<<"${CONSENT}")"
if [[ -n "${URL}" ]]; then
  printf '%s\n' "${URL}" > "${CACHE}/consent-url.txt"
  command -v xdg-open >/dev/null && [[ -n "${DISPLAY:-}" ]] && { xdg-open "${URL}" >/dev/null 2>&1 || true; }
fi

banner "3. Alice opens the link and grants read-only access (gmail.readonly)"
echo "   (full link: .cache/consent-url.txt)"
printf '%s' "   waiting for consent "
# Watch the app's log; asking /consent again would start a new attempt.
until kubectl -n email-app logs deploy/email-app --since-time="${START}" 2>/dev/null | grep -q '"consent finalized"'; do
  printf '.'; sleep 2
done
echo " ✓"
pause

banner "4. Same call, same sandbox"
pe "agent search"
banner "   ...and the sandbox still holds no token"
pe "agent audit"

banner "5. The agent tries to trash a message"
pe "agent trash"
banner "   Google refuses: the token is scoped to gmail.readonly"

banner "6. What the logs say"
p "kubectl logs -n ate-system deploy/atenet-egress -c envoy | grep gmail"
kubectl -n ate-system logs deploy/atenet-egress -c envoy --since-time="${START}" 2>/dev/null \
  | grep '"leg":"mitm"' | grep gmail.googleapis.com | tail -2 \
  | jq -C -c '{actor, authority, method, path: (.path|sub("\\?.*";"")), status}' || true
p "kubectl logs -n ate-system deploy/authmanager-provider | grep injected"
kubectl -n ate-system logs deploy/authmanager-provider --since-time="${START}" 2>/dev/null \
  | grep 'credential injected' | tail -1 | jq -C -c '{msg, actor, user, scope}' || true
pause
