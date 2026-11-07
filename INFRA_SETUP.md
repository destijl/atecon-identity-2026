# Infrastructure setup log

Every infrastructure-modifying command for the OAuth/Gmail demo, in order. Secrets are never written here.

Project `gcastle-testing-402921`, cluster `substrate-gateway-test` (us-central1-c, existing GKE Standard, 2x c3-standard-4, Workload Identity on). OAuth client project `oauth-testing-508220`.

## 1. OAuth client (console, done by gcastle, 2026-11-05)

In `oauth-testing-508220`:

1. APIs & Services → Library → Gmail API → Enable.
2. Google Auth Platform → Audience: External, Testing; test user = the account in `DEMO_EMAIL` (demo.env).
3. Data Access → add scope `https://www.googleapis.com/auth/gmail.readonly`.
4. Clients → Create client → Web application, redirect URI
   `https://agentidentitycredentials.googleapis.com/v1/projects/gcastle-testing-402921/locations/us-central1/authProviders/gmail-connector/oauthcallback`.

## 2. Auth Manager provider (done by gcastle, 2026-11-05)

```bash
read -r -p "Client ID: " CID; read -r -s -p "Client secret: " CSEC; echo
gcloud agent-identity auth-providers create gmail-connector \
  --project=gcastle-testing-402921 --location=us-central1 \
  --three-legged-oauth-client-id="$CID" \
  --three-legged-oauth-client-secret="$CSEC" \
  --three-legged-oauth-authorization-url=https://accounts.google.com/o/oauth2/v2/auth \
  --three-legged-oauth-token-url=https://oauth2.googleapis.com/token \
  --three-legged-oauth-default-continue-uri=https://example.invalid/oauth/continue \
  --allowed-scopes=https://www.googleapis.com/auth/gmail.readonly
unset CID CSEC
```

## 3. Wipe old workloads on the cluster (2026-11-05)

Use a private kubeconfig; never the shared `~/.kube/config`.

```bash
export KUBECONFIG=$PWD/.cache/kubeconfig
gcloud container clusters get-credentials substrate-gateway-test \
  --zone us-central1-c --project=gcastle-testing-402921
kubectl delete pod -n default spike-runner node-debugger-gke-substrate-gateway-te-default-pool-c5773cf6-0sok-ln2d8
kubectl delete workerpool -n spike-workers spike-pool
kubectl delete ns spike-echo spike-secrets spike-workers
```

## 4. Artifact Registry repo (2026-11-05)

```bash
gcloud artifacts repositories create atecon-demo --repository-format=docker \
  --location=us-central1 --project=gcastle-testing-402921 \
  --description="atecon identity talk OAuth demo images"
```

## 5. Clear the old Substrate install (2026-11-05)

`ate-setup delete all` at 274605f1, then two leftovers that blocked reinstall:

```bash
# from the Substrate worktree at 274605f1
go run ./cmd/ate-setup --context "$(kubectl config current-context)" delete all
# A GKE Gateway (old ingress experiment) held ate-system in Terminating:
kubectl -n ate-system patch gateway substrate-ingress-gw --type=merge -p '{"metadata":{"finalizers":[]}}'
# Old v1alpha1 CRDs can't be upgraded in place:
kubectl patch sandboxconfig gvisor-default --type=merge -p '{"metadata":{"finalizers":[]}}'
kubectl delete crd sandboxconfigs.ate.dev actortemplates.ate.dev
```

Leftover to clean up later (needs approval): forwarding rule `gkegw1-2oxw-ate-system-substrate-ingress-gw-19cd6m93xkg9` (us-central1) from that Gateway.

## 6. Install Substrate + deploy the demo

`scripts/up.sh` (reads `demo.env`). It runs, in order:

- `ate-setup deploy ate-system` at `SUBSTRATE_COMMIT` with
  `--credential-provider='[{"name":"k8s.io"},{"name":"authmanager.demo","address":"authmanager-provider.ate-system.svc:50051"}]'`,
  `--sandbox-classes=gvisor`, `--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/ate-system/sa/atenet-client`,
  `SNAPSHOT_LOCATION=gs://snapshot-substrate-gateway-test-gcastle-testing-402921/`, `SNAPSHOT_TRANSFER_GSA_NAME=ate-snapshot-transfer`,
  images built with ko into `us-central1-docker.pkg.dev/gcastle-testing-402921/atecon-demo/substrate`.
- Builds and pushes `agent`, `email-app`, `authmanager-provider` to `.../atecon-demo/`.
- Applies `deploy/authmanager-provider.yaml`, `deploy/email-app.yaml` (creates LoadBalancer `email-app-callback`), `deploy/workerpool.yaml`.
- `gcloud agent-identity auth-providers update gmail-connector --three-legged-oauth-default-continue-uri=http://<LB IP>/oauth/continue`
- `gcloud beta agent-identity auth-providers add-iam-policy-binding gmail-connector --role roles/agentidentity.user` for
  `principal://iam.googleapis.com/projects/791486336906/locations/global/workloadIdentityPools/gcastle-testing-402921.svc.id.goog/subject/ns/ate-system/sa/authmanager-provider`
  and `.../subject/ns/email-app/sa/email-app`.

`demo/reset.sh` then creates the atespace, ActorTemplate, actor and EgressPolicy (demo-owned objects only) and revokes the user's Auth Manager grant.

## 7. Stale node image cache (2026-11-05)

atelet at 274605f1 crash-looped: `image cache at "/var/lib/ate/image-cache" has layout version "2" and this atelet supports "3"`. Per node, a one-shot pod (hostPath `/var/lib/ate`, `nodeName` pinned, alpine) ran `rm -rf /ate/image-cache`, then `kubectl -n ate-system delete pod -l app=atelet`. Only needed when reinstalling over an older Substrate build.

## 8. Delete leftover Gateway forwarding rule (approved by gcastle, 2026-11-06)

```bash
gcloud compute forwarding-rules delete gkegw1-2oxw-ate-system-substrate-ingress-gw-19cd6m93xkg9 \
  --region us-central1 --project=gcastle-testing-402921 --quiet
```

## 9. Let atelet pull actor images from atecon-demo (2026-11-06)

atelet pulls actor images as its own KSA via Workload Identity (not the node SA):

```bash
gcloud artifacts repositories add-iam-policy-binding atecon-demo --location us-central1 \
  --project gcastle-testing-402921 --role roles/artifactregistry.reader \
  --member "principal://iam.googleapis.com/projects/791486336906/locations/global/workloadIdentityPools/gcastle-testing-402921.svc.id.goog/subject/ns/ate-system/sa/atelet"
```

Note (step 8): the forwarding-rule delete failed because authz policy `substrate-test-fr-deny` (us-central1, created 2026-10-09) still references it. Not yet deleted.

## 10. Remove leftover Agent Gateway experiment (approved by gcastle, 2026-11-06)

```bash
gcloud network-security authz-policies delete substrate-test-fr-deny --location us-central1 --project gcastle-testing-402921 --quiet
gcloud network-services agent-gateways delete substrate-self-managed-gw --location us-central1 --project gcastle-testing-402921 --quiet
gcloud compute forwarding-rules delete gkegw1-2oxw-ate-system-substrate-ingress-gw-19cd6m93xkg9 --region us-central1 --project gcastle-testing-402921 --quiet
```

## 11. HTTPS consent callback on Cloud Run (2026-11-06)

The browser must land on a TLS URL without warnings. The email app stays on GKE behind an internal load balancer; a Cloud Run front door (Google-managed cert) forwards only `GET /oauth/continue` to it over direct VPC egress. All of this is in `scripts/up.sh`:

```bash
# deploy/email-app.yaml: Service email-app-callback, annotation networking.gke.io/load-balancer-type: Internal
# (an extra email-app-callback-ilb Service was created then deleted on 2026-11-07 with approval)
gcloud artifacts repositories add-iam-policy-binding atecon-demo --location us-central1 --project gcastle-testing-402921 \
  --role roles/artifactregistry.reader \
  --member serviceAccount:service-791486336906@serverless-robot-prod.iam.gserviceaccount.com
gcloud run deploy email-app-callback --image us-central1-docker.pkg.dev/gcastle-testing-402921/atecon-demo/callback-proxy@sha256:... \
  --region us-central1 --project gcastle-testing-402921 --allow-unauthenticated \
  --network default --subnet default --vpc-egress private-ranges-only \
  --set-env-vars TARGET_URL=http://<email-app-callback internal IP> --min-instances 1 --max-instances 2
gcloud agent-identity auth-providers update gmail-connector --project gcastle-testing-402921 --location us-central1 \
  --three-legged-oauth-default-continue-uri=https://email-app-callback-7blvptsmvq-uc.a.run.app/oauth/continue
```

Ingress allowlist (re-applied via ate-setup): `--ingress-allowed-spiffe-ids=spiffe://cluster.local/ns/ate-system/sa/atenet-client,spiffe://cluster.local/ns/email-app/sa/email-app`.
