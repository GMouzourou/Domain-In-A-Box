# Kubernetes

The Helm chart in [`charts/domain-in-a-box`](../charts/domain-in-a-box) installs
one domain controller as a single-replica StatefulSet, plus the Stork server
and, optionally, a bundled PostgreSQL database. For two controllers, see
[Multi-controller Kubernetes](multi-controller.md).

## Prerequisites

- Kubernetes (tested with RKE2 and kind) and Helm 3.
- [Multus CNI](https://github.com/k8snetworkplumbingwg/multus-cni) with the
  macvlan plugin. The chart creates the NetworkAttachmentDefinition.
- A `ReadWriteOnce` storage class. Set `persistence.storageClass` if your
  cluster has no default.
- A free LAN address for the controller, and the name of the node interface
  on that LAN (for example `eth0`). It must exist on every node the pod can
  run on, or you must pin the pod with `nodeSelector`.
- Controller and Stork certificates. See [Certificates](certificates.md).

## Install

### 1. Create the namespace and Secrets

```bash
kubectl create namespace domain-in-a-box

: "${DIB_DOMAIN_ADMIN_PASSWORD:?}" "${STORK_DATABASE_PASSWORD:?}" "${DIB_LDAP_PASSWORD:?}"
kubectl -n domain-in-a-box create secret generic domain-in-a-box-credentials \
  --from-literal=domain-admin-password="$DIB_DOMAIN_ADMIN_PASSWORD" \
  --from-literal=stork-db-password="$STORK_DATABASE_PASSWORD" \
  --from-literal=stork-ldap-password="$DIB_LDAP_PASSWORD"
```

Then create the two TLS Secrets as shown in
[Certificates](certificates.md#kubernetes-secrets). The chart does not create
credentials and has no default passwords.

### 2. Write a values file

```yaml
# my-values.yaml
domain:
  realm: AD.HOME.ARPA
  name: AD
  dhcpPool: 192.168.1.100-192.168.1.199
  dnsForwarders: "1.1.1.1; 8.8.8.8;"

multus:
  masterInterface: eth0          # node interface on the LAN
  address: 192.168.1.1/24        # controller address and prefix
  gateway: 192.168.1.254

persistence:
  storageClass: ""               # empty uses the cluster default
  size: 10Gi

# Optional: keep the controller on one node
# nodeSelector:
#   kubernetes.io/hostname: node1
```

See the [configuration reference](configuration.md#helm-values) for every
value.

### 3. Install the chart

```bash
helm install domain-in-a-box ./charts/domain-in-a-box \
  --namespace domain-in-a-box -f my-values.yaml
kubectl -n domain-in-a-box rollout status statefulset/domain-in-a-box
```

First-time provisioning takes a few minutes. The pod becomes Ready when core
bootstrap has completed and LDAP, Kerberos and BIND are listening.

### 4. Verify

```bash
kubectl -n domain-in-a-box get pods,pvc
kubectl -n domain-in-a-box get pod domain-in-a-box-0 \
  -o jsonpath='{.metadata.annotations.k8s\.v1\.cni\.cncf\.io/network-status}'
dig @192.168.1.1 _ldap._tcp.ad.home.arpa SRV     # from a LAN client
```

The network-status annotation should list a `net1` interface with the
controller address.

## Accessing Stork

The Stork UI Service is ClusterIP. Use an Ingress, or port-forward:

```bash
kubectl -n domain-in-a-box port-forward service/domain-in-a-box-stork-server 8443:443
```

Then open `https://localhost:8443/`. Your browser will warn that the
certificate name does not match `localhost`. Sign in with an Active Directory
account: Domain Admins get administrator access, and other Domain Users get
read-only access.

The Stork agent in the controller pod registers through a headless Service on
port `8081`, using its stable in-cluster DNS name. No LAN address is needed
for the Stork server.

## External PostgreSQL

To use an existing PostgreSQL server instead of the bundled one:

```yaml
database:
  external:
    enabled: true
    host: postgresql.databases.svc.cluster.local
    port: 5432
    name: stork
    user: stork
    passwordSecret:
      name: postgres-credentials
      key: password
    tls:
      sslMode: verify-full
      rootCertSecretName: postgres-ca   # Secret with the CA under rootCertKey (ca.crt)
```

With `verify-full`, the host name must match a DNS SAN in the server
certificate (or an IP SAN when `host` is an IP address). For client
certificate authentication, set `tls.clientCertSecretName`. The `disable` and
`require` modes need `tls.allowInsecure: true` and do not verify the server.

## What the chart deploys

| Resource | Purpose |
| --- | --- |
| StatefulSet (1 replica) | The controller pod, with a Multus LAN interface and one `data` PersistentVolumeClaim |
| NetworkAttachmentDefinition | The macvlan attachment (disable with `multus.createNAD: false` to use your own) |
| Headless Service `<fullname>-svc` | Stable in-cluster DNS name for the controller (used for LDAPS from Stork) |
| Headless Service `<fullname>-stork-agent` | Stork agent callback on port `8081` |
| Deployment and Service `<fullname>-stork-server` | Stork UI and API |
| PostgreSQL StatefulSet and Service `<fullname>-postgresql` | Bundled Stork database (unless `database.external.enabled`) |

Do not scale the StatefulSet above one replica. A second controller is a
separate release with its own identity. See
[Multi-controller Kubernetes](multi-controller.md).

## Behaviour and limits

- **Pod restart:** the controller restarts on the same data. Clients see a
  short outage of all services.
- **Node failure:** the pod can only restart where its volume is available.
  With node-local storage it waits for the node to return. With replicated
  storage, Kubernetes may restart it on another node after the failed node's
  pod is removed. Make sure the failed node is really down first: two copies
  of one controller must never run.
- **Monitoring outage:** the domain keeps running. Stork registration
  retries in the background.
- **Readiness** checks the core bootstrap marker and the LDAP, Kerberos and
  BIND listeners. It does not prove that DHCP leases, DDNS updates or client
  logins work.
- The container runs privileged. Check that this fits your cluster's
  security policy.

For upgrades, certificate rotation and backups, see [Operations](operations.md).
