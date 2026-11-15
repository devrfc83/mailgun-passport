# Mailgun Passport on Kubernetes

A Deployment, a Service and a NetworkPolicy for a cluster such as DigitalOcean Kubernetes. They are plain manifests with a `kustomization.yaml`; there is no Helm chart, since a relay this small has few values to template.

```bash
kubectl create namespace mail
kubectl -n mail create secret generic mailgun-passport \
  --from-literal=mailgun-api-key=key-... \
  --from-literal=smtp-password="$(openssl rand -hex 24)"
# Edit MAILGUN_DOMAIN in deployment.yaml, then:
kubectl apply -n mail -k contrib/kubernetes
```

Applications send to `mailgun-passport.mail.svc:587`, user `relay`, with that password. A pod needs the label `mailgun-passport-client: "true"` to reach it, once the NetworkPolicy is in force.

- **Secrets** are mounted as files and read through `MAILGUN_API_KEY_FILE` and `SMTP_PASSWORD_FILE`. After rotating one, update the Secret and run `kubectl -n mail rollout restart deploy/mailgun-passport`. (The relay reloads such files on `SIGHUP`, but a distroless image has no `kill` to send it from `kubectl exec`; a rolling restart is just as clean here, since the Service keeps routing to the old pod until the new one is ready.)
- **Probes** use `/health` for liveness and `/ready` for readiness, as the [README](../../README.md#health-checks) describes. A Mailgun outage makes the pod not ready, so the Service stops routing to it and clients cannot connect and retry. With one replica, that is the same as the relay answering `451`.
- **Metrics** are on port 8080 at `/metrics`, with the usual `prometheus.io/*` annotations. Allow your Prometheus in the NetworkPolicy.
- **The queue** is not enabled here: it needs a disk that outlives the pod. To use it, replace the Deployment by a StatefulSet with one replica and a `volumeClaimTemplates` entry mounted at `/var/lib/mailgun-passport`, and set `QUEUE_DIR` to that path. Never run two pods on one volume.
- **TLS.** The manifests allow `AUTH` in clear text, which stays inside the cluster. For STARTTLS, mount a certificate Secret and set `SMTP_TLS_CERT_FILE` and `SMTP_TLS_KEY_FILE`; the relay reloads it when the files change.

Pin the image tag in `kustomization.yaml` to a release (`0.3` follows the latest `0.3.x`), and verify its signature as the [README](../../README.md#verify-a-download) shows.
