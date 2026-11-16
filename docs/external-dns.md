# Automatic Kubernetes application DNS

The optional ExternalDNS bridge registers eligible Ingress names directly in
your AD DNS domain, for example `new-service.domain.home.arpa`. It is a separate
Helm release: installing or upgrading it does not restart your controllers.
It does not replace DHCP registration or take over existing manual records.

## Architecture and supported records

One Deployment contains the official ExternalDNS controller and a DIB bridge
listening only on `127.0.0.1:8888`. There is no webhook Service or Ingress.
ExternalDNS receives a projected, read-only Kubernetes Ingress token; only the
bridge receives the dedicated DNS password, DNS CA and temporary Kerberos cache.
The containers do not share process namespaces or credential mounts.

The first implementation supports flat application names, IP-valued A/AAAA
records and prefixed ownership TXT records. CNAME-valued load balancers,
wildcards, nested application names, other record types and other Kubernetes
sources are not supported. Invalid inputs fail visibly. Manual/conflicting names
are excluded with warning logs so unrelated new registrations can continue.
Explicitly protected names are also excluded from desired endpoints; they do
not abort reconciliation of unrelated eligible Ingresses.

ExternalDNS uses Ingress hostnames when supplied; otherwise the template uses
the Ingress name and selected domain. Load-balancer IP addresses must already
appear in Ingress status and fall within the configured CIDRs. Ingress names
with dots or excessively long labels need an eligible explicit hostname.

## Authority and prerequisites

Provision a dedicated ordinary account, not Administrator or a general-purpose
user. The controller-local `provision` command:

- refuses an existing account with different ownership metadata;
- never silently rotates a password or changes group membership;
- adds account-specific DNS ACL entries inside a local database transaction,
  preserving unrelated explicit ACEs;
- allows creation in the existing managed zone, protects existing manual nodes,
  and denies direct access to other existing AD DNS zones and their nodes;
- verifies authenticated, hostname-checked LDAPS listing afterwards.

**These ACLs are not a permanent, name/type-limited AD capability.** A raw
credential can create broader DNS content in the managed zone than the bridge
allows, and retains ordinary domain-account permissions. Existing protected
nodes are guarded, but an absent reserved name is protected by bridge policy,
not a permanent AD object ACL. New zones, ownership/ACL changes and additional
group membership require review and reconciliation before delegating access.
Do not share or reuse this credential. The account follows the domain's password
policy; arrange explicit rotation before expiry.

Anyone allowed to create or edit watched Ingresses gains authority over eligible
new DNS names. Limit `watchNamespace` and/or `ingressClassNames` when appropriate.
Different namespaces can request the same flat name; namespace separation alone
is not DNS tenant isolation. Keep one stable owner ID and one active Deployment
for the chosen ownership scope.

Prerequisites are reachable controller LDAP/Kerberos/DNS services, trusted
controller certificates with matching FQDNs, an existing AD DNS zone and an
Ingress controller publishing IP-valued status. CA/trust rotation is a separate
procedure and currently requires a bridge rollout.

## Install

The bridge image is independently packaged:

```bash
docker pull ghcr.io/gmouzourou/domain-in-a-box-external-dns-bridge:0.6.0
```

Prepare a protected password file and a bridge JSON configuration containing
`realm`, `username`, `ownerID`, `controllers`, `allowedNetworks`,
`passwordFile` and `caFile`. Each controller entry has `hostname` and `address`.
Optional fields are `zone` (defaults to the lower-cased realm; set it only to
publish into a sub-zone), `protectedNames`, `txtPrefix`, `defaultTTL` and
`listen`.
The listener must remain an explicit loopback address.

Run the packaged command as root **inside the selected controller**, with the
configuration, password and CA staged at their declared paths:

```bash
dib-external-dns-bridge provision --bridge-config /run/dib-dns/config.json
```

Review its outcome and replication before deploying the controller. Keep secret
material out of command arguments, Helm values, source control and logs.
An incorrect existing password produces an explicit verification failure; the
command does not reset it.

Create the dedicated Kubernetes namespace and Secrets from files:

```bash
kubectl create namespace application-dns
kubectl -n application-dns create secret generic dns-credentials \
  --from-file=password=/private/path/bridge-password
kubectl -n application-dns create secret generic dns-trust \
  --from-file=ca.crt=/private/path/domain-ca.crt
```

Configure a values file:

```yaml
realm: DOMAIN.HOME.ARPA
username: dib-edns
ownerID: home-ingress
controllers:
  - hostname: dc1.domain.home.arpa
    address: 192.168.1.8
  - hostname: dc2.domain.home.arpa
    address: 192.168.1.9
allowedNetworks:
  - 192.168.1.0/24
protectedNames:
  - gitea
watchNamespace: applications
credentials:
  existingSecret: dns-credentials
trust:
  existingSecret: dns-trust
```

Install the separate chart from this checkout:

```bash
helm upgrade --install application-dns \
  charts/domain-in-a-box-external-dns-bridge \
  --namespace application-dns --values /private/path/dns-values.yaml --wait
```

Protect existing infrastructure explicitly as needed. The apex, controller
hostnames, `localhost` and `wpad` are always excluded. With the default
`dib-edns-` TXT prefix, ExternalDNS creates separate `dib-edns-a-<name>` and
`dib-edns-aaaa-<name>` ownership records. The application label is limited to
49 bytes so both ownership labels fit within DNS's 63-byte limit. Ownership is
checked independently for each address family; an IPv4 marker cannot adopt an
unowned IPv6 record.

## Operation and recovery

Watch both container logs and readiness. Failed authentication, trust or listing
never becomes a successful empty DNS result. Liveness checks only the local
listener; a controller outage should not restart the bridge.

Updates pair address records with exact ownership/value prerequisites in one
GSS-TSIG transaction. Concurrent changes fail safely. A fresh authenticated
snapshot recognizes an already-applied request after an ambiguous failure.
Neither the controller's successful request nor provisioning alone is a
replication acceptance test: verify answers from both controllers.

Deleting a watched Ingress removes only matching owned data. Existing manual
names are never implicitly migrated. If a manual service needs automation,
review ownership, permissions and conflicting records separately; do not forge
a TXT marker to adopt it.

For credential rotation, explicitly change the dedicated domain account
password, update the Secret from a protected file, and verify readiness and
synchronization. The bridge reads projected passwords for each backend operation.
Trust configuration is loaded at startup, so CA changes need a controlled rollout.
