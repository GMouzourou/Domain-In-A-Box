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

Stork's LDAP connection uses the controller's internal Service. A supervised
LDAPS relay listens only on the pod IP's port 636 and forwards encrypted bytes
to Samba on loopback. It does not terminate TLS or change Samba's LAN bindings,
AD identity or DNS registrations. Certificates must still cover the Service
name used by Stork. The relay is unnecessary when Samba already binds the
primary pod interface; it is disabled outside Kubernetes by default.

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
| Optional NetworkPolicy `<fullname>` | Release-scoped primary pod-network ingress and egress |

Do not scale the StatefulSet above one replica. A second controller is a
separate release with its own identity. See
[Multi-controller Kubernetes](multi-controller.md).

## Container security

The controller defaults to `security.privileged: false`, drops all capabilities,
then grants the explicit profile in the
[StatefulSet template](../charts/domain-in-a-box/templates/statefulset.yaml).
It uses RuntimeDefault seccomp and does not mount a service-account token.
The controller still runs as root with a writable filesystem.

`SYS_ADMIN` is required for Samba SYSVOL ACL provisioning. It remains broad;
this profile is not a restricted/non-root security boundary or a claim that
every capability is mathematically minimal. Kea needs `NET_ADMIN`/`NET_RAW`,
and the Stork agent needs `DAC_READ_SEARCH`/`SYS_PTRACE` executable file
capabilities after switching Unix users, so the controller intentionally permits
privilege escalation. Chrony runs with `-x`, without `SYS_TIME` or host-clock changes.

The Stork server also remains root: startup installs CA trust, repairs file
ownership and writes configuration. It drops all capabilities except
`CHOWN`, `DAC_OVERRIDE`, `FOWNER` and `NET_BIND_SERVICE`, disallows escalation
and uses RuntimeDefault seccomp. Stork and bundled PostgreSQL do not mount
service-account tokens. PostgreSQL otherwise retains its existing filesystem/user behavior.

Set `security.privileged: true` only for a deliberately reviewed compatibility
exception. That controller also omits the normal seccomp profile to preserve
legacy privileged behavior. Pod Security Admission's restricted policy is not
compatible with the controller or Stork; do not exempt unrelated workloads.

## Optional NetworkPolicy

`networkPolicy.enabled: true` selects this release's pods. Both ingress and
egress are restricted on the primary pod network. Built-in rules allow DIB-name
pods in the same namespace to communicate on TCP 636 (LDAPS), 8081 (agent
callback), 443/80 (Stork) and 5432 (bundled PostgreSQL). DNS TCP/UDP 53 and NTP
UDP 123 egress are allowed to any destination.

Add rules for your actual clients, monitoring, ingress controller and external
database before enabling the policy. For example, adapt these namespaces and
pod labels to your deployment:

```yaml
networkPolicy:
  enabled: true
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: monitoring
          podSelector:
            matchLabels:
              app.kubernetes.io/name: prometheus
      ports:
        - {protocol: TCP, port: 9922}
        - {protocol: TCP, port: 9119}
        - {protocol: TCP, port: 9547}
  egress:
    - to:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: postgres
          podSelector:
            matchLabels:
              app.kubernetes.io/name: postgresql
      ports:
        - {protocol: TCP, port: 5432}
```

This example does not allow Stork UI ingress or pod-network AD clients; add
their selected sources and required ports separately. Other policies selecting
the same pods can add allowances because Kubernetes NetworkPolicies are additive.
Verify actual allowed and denied connections with an enforcing CNI. Default
kind networking alone does not enforce policies.

**Multus/macvlan LAN traffic is outside this policy's protection.** Protect that
network through LAN segmentation/firewalls and service authentication. Enabling
this policy is not a DHCP, Kerberos, SMB or replication firewall on the LAN.

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
- **Liveness** checks the same listeners, after the startup probe, and
  restarts the pod only after about three minutes of failure. A crash-looping
  core process restarts the pod sooner; see
  [Automatic recovery](operations.md#automatic-recovery).
- The root controller retains broad required capabilities. Check that the
  [security profile](#container-security) fits your cluster's security policy.

For upgrades, certificate rotation and backups, see [Operations](operations.md).
