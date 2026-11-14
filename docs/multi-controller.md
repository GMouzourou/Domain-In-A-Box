# Multi-controller Kubernetes (experimental)

> **Experimental and unsupported.** The two-controller mode has passed
> development-lab checks on a three-node RKE2 cluster, but full client
> acceptance, DHCP failover, storage failure, cold start, power loss and
> backup recovery have not been validated. Do not rely on it for production.
> See [Validation status](#validation-status).

Two domain controllers run on separate physical hosts. If one host fails,
authentication, directory lookups and DNS continue on the other without
operator action. Services with a single owner (SYSVOL writes and DHCP lease
allocation) degrade predictably and never move automatically.

## How it works

- **Two pinned controllers.** A **primary** provisions the domain. A
  **secondary** joins it as an additional domain controller. Each has its own
  identity, LAN address, data volume and physical host. Neither is ever
  rescheduled to another host or started on a copy of the other's data.
- **AD, DNS and Kerberos are multi-master.** Both controllers accept reads and
  writes, and replication converges after an outage.
- **SYSVOL has one writer.** The primary is the only place Group Policy files
  can be changed. It pushes a sanitized, read-only copy to the secondary over
  SSH. The secondary's SYSVOL and NETLOGON shares are always read-only.
- **DHCP has one server.** With DHCP HA enabled, Kea runs as hot-standby: the
  secondary keeps a synchronized copy of the leases but only answers clients
  after an operator enables it.
- **No fencing.** Domain-In-A-Box cannot prove that an unreachable controller
  has stopped, so it never moves authority automatically. The reasons are
  explained in the [design notes](multi-controller-design.md).

## Service behaviour and consequences

"Partition" means the controllers cannot reach each other, but both are still
running and may still reach clients. Without fencing, a controller cannot tell
a partition from a host loss, so both behave the same.

| Service | One controller lost or partitioned | When it returns | Operator action | Consequences and risks |
| --- | --- | --- | --- | --- |
| AD LDAP, Kerberos, AD DNS | Both controllers keep serving. Clients find the survivor through DNS SRV records. | Replication converges automatically. | None | Changes held only by a permanently destroyed controller are lost. They can include account disablement, group removal and password resets, so review security-relevant changes after a permanent loss. Conflicting concurrent changes follow AD's last-writer-wins rules; name collisions create conflict (`CNF`) objects. |
| Group Policy reads, logon scripts | Both serve their SYSVOL copy. The secondary serves the last synchronized version. | The primary resumes one-way synchronization. | None | During a primary outage, clients may receive out-of-date policy. Changes made just before the outage may be missing on the secondary. |
| Group Policy edits | Only the primary accepts SYSVOL and NETLOGON file writes. The secondary rejects them at all times. | Synchronization resumes. | Do not edit Group Policy while the primary is down. | A Group Policy Management operation writes AD metadata separately from its SYSVOL files. If it targets the secondary, the AD part may succeed while the file part fails, leaving them inconsistent. Repeat the edit on the primary after recovery. |
| DHCP (with DHCP HA) | **Primary lost:** existing leases continue until they expire; renewals and new clients get no answer. **Secondary lost:** no effect. | Withdraw the secondary's authority before restarting the primary's DHCP service, then confirm lease synchronization. | If the primary is down longer than clients can tolerate: confirm its host is off, then run the [DHCP takeover](operator-procedures.md#take-over-dhcp-on-the-secondary). | Without action, clients lose connectivity as leases expire. Enabling the secondary while the primary is still running can hand out duplicate addresses. Returning authority causes a short, deliberate DHCP outage. |
| DNS servers given to DHCP clients | Clients receive only the serving controller's address. | No change. | None | DHCP clients do not automatically fail over DNS to the other controller. Tracking: [#45](https://github.com/GMouzourou/Domain-In-A-Box/issues/45). |
| DDNS from DHCP | Each controller updates its own AD DNS with its own service account. | Replication converges. | None | Records updated on one controller may briefly differ between controllers. |
| FSMO roles | Roles stay with their holder (normally the primary). | No change. | Only after permanent loss: seize the roles. | While the PDC emulator is down, there is no domain time-hierarchy root and Group Policy Management must target the secondary explicitly. Schema and domain-naming changes, and RID pool refills (after about 500 new objects), wait for the holder. |
| Time (NTP) | Each controller serves time from its host's clock. | No change. | None | Each host needs its own time synchronization; Chrony runs with `-x` and does not set the host clock. |
| Monitoring (Stork, PostgreSQL) | Monitoring may be unavailable; the domain keeps running. | Registration and exporters recover. | None for domain services. | No visibility while monitoring is down. PostgreSQL failover is not tested. |
| A controller permanently lost | The survivor carries all automatic services. | Never returns with the same identity. | Remove the dead controller and join a fresh one. | No redundancy until a replacement joins. If the primary is lost, Group Policy cannot be edited until promotion is supported ([#40](https://github.com/GMouzourou/Domain-In-A-Box/issues/40)). |

### Accepted consequences

- Group Policy may be out of date on the secondary during a primary outage.
- DHCP keeps existing leases but cannot issue or renew addresses during a
  primary outage until the operator acts.
- Taking over DHCP while the primary is still running can cause duplicate
  addresses. This is the one step where the operator must be certain.
- Changes held only by a permanently destroyed controller are lost.
- A failed controller stays down until its own host returns.
- **Never restore or revert one controller's volume from a snapshot or backup
  into a running domain.** It causes USN rollback and silent replication
  divergence. Replace the controller by joining a new one instead.

## Requirements

- Kubernetes with Multus and at least two physical nodes on the same LAN.
  A third node is useful for monitoring and testing.
- Two free LAN addresses, one per controller.
- A `ReadWriteOnce` storage class. Each controller's volume must stay with its
  node. If replicated storage (for example Longhorn) backs the volume, never
  use it to start a controller on another host or to revert a snapshot. A
  storage replica is not an additional domain controller.
- Independent time synchronization on every host.
- The [host shim](networking.md#host-to-container-limitation) on each node
  that must reach its local controller.
- A controller certificate per controller. With DHCP HA, each must include
  the controller's LAN IP address. See [Certificates](certificates.md).

The chart enforces the pairing rules. Each paired release must pin its node
with `nodeSelector.kubernetes.io/hostname`, enable persistence and Multus, and
keep one replica. The secondary must disable its own Stork server and point
to the primary's, and its pod name must be 15 characters or fewer (for
example `fullnameOverride: dib-secondary` gives `dib-secondary-0`).

## Setup

The examples use namespace `dib`, primary `192.168.1.8` on `node1` and
secondary `192.168.1.10` on `node2`.

### 1. Create SYSVOL synchronization keys

The secondary pulls SYSVOL from the primary over SSH, using a dedicated
key-only account restricted to read-only `rrsync`, and a pinned host key. In
a protected directory:

```bash
umask 077
ssh-keygen -q -t ed25519 -N '' -C dib-sysvol-client -f id_ed25519
ssh-keygen -q -t ed25519 -N '' -C dib-sysvol-writer -f ssh_host_ed25519_key
printf '192.168.1.8 %s\n' "$(cut -d' ' -f1,2 ssh_host_ed25519_key.pub)" > known_hosts

kubectl -n dib create secret generic dib-sysvol-writer \
  --from-file=ssh_host_ed25519_key --from-file=id_ed25519.pub
kubectl -n dib create secret generic dib-sysvol-reader \
  --from-file=id_ed25519 --from-file=known_hosts
```

Use `[192.168.1.8]:<port>` in `known_hosts` for a non-default SSH port. Remove
the local key copies when they are no longer needed, and keep private keys
out of source control.

### 2. Create credentials and certificates

Create one credentials Secret (shared by both releases) and a controller TLS
Secret per controller, as described in [Kubernetes](kubernetes.md#1-create-the-namespace-and-secrets)
and [Certificates](certificates.md#kubernetes-secrets).

### 3. Install the primary

```yaml
# primary.yaml
fullnameOverride: dib-primary
domain:
  realm: AD.HOME.ARPA
  name: AD
  controllerMode: primary
  dhcpEnabled: true
  dhcpPool: 192.168.1.100-192.168.1.199
sysvolSync:
  enabled: true
  secretName: dib-sysvol-writer
  peerAddress: 192.168.1.10
multus:
  networkName: dib-primary-macvlan
  masterInterface: eth0
  address: 192.168.1.8/24
  gateway: 192.168.1.254
tls:
  controller:
    secretName: dib-primary-controller-tls
  storkServer:
    secretName: dib-primary-stork-server-tls
nodeSelector:
  kubernetes.io/hostname: node1
```

```bash
helm install dib-primary ./charts/domain-in-a-box -n dib -f primary.yaml
kubectl -n dib rollout status statefulset/dib-primary
```

### 4. Install the secondary

Wait until the primary is Ready, then:

```yaml
# secondary.yaml
fullnameOverride: dib-secondary
domain:
  realm: AD.HOME.ARPA
  name: AD
  controllerMode: secondary
  joinServer: 192.168.1.8
  dhcpEnabled: false
sysvolSync:
  enabled: true
  secretName: dib-sysvol-reader
  writerAddress: 192.168.1.8
storkServer:
  enabled: false
  host: dib-primary-stork-server.dib.svc.cluster.local
multus:
  networkName: dib-secondary-macvlan
  masterInterface: eth0
  address: 192.168.1.10/24
  gateway: 192.168.1.254
tls:
  controller:
    secretName: dib-secondary-controller-tls
  storkServer:
    secretName: dib-primary-stork-server-tls
nodeSelector:
  kubernetes.io/hostname: node2
```

```bash
helm install dib-secondary ./charts/domain-in-a-box -n dib -f secondary.yaml
kubectl -n dib rollout status statefulset/dib-secondary
```

The join creates a new directory identity and replicates the directory. Samba
can take up to 15 minutes to create replication agreements after a join.

If a join fails partway, the secondary refuses to retry on the same volume.
Inspect the domain, remove any partial controller entry, and start again with
a fresh volume.

### 5. Optional: DHCP HA

DHCP HA is the least validated part of this mode
([#43](https://github.com/GMouzourou/Domain-In-A-Box/issues/43)). Enable it
only on an isolated network, or once you have confirmed that the DHCP pool
does not overlap another server's range. Add to **both** values files:

```yaml
dhcpHA:
  enabled: true
  primaryAddress: 192.168.1.8
  standbyAddress: 192.168.1.10
```

Keep `domain.dhcpEnabled: true` on the primary and `false` on the secondary.

### 6. Verify

```bash
kubectl -n dib exec dib-primary-0 -- samba-tool drs showrepl
kubectl -n dib exec dib-secondary-0 -- samba-tool drs showrepl
kubectl -n dib exec dib-primary-0 -- samba-tool ntacl sysvolcheck
kubectl -n dib exec dib-secondary-0 -- samba-tool ntacl sysvolcheck
kubectl -n dib exec dib-secondary-0 -- dib-network-core-ctl dhcp-ha status   # with DHCP HA
```

Both controllers should report successful inbound replication for all naming
contexts. The SYSVOL checks should pass on both.

## FSMO roles

The primary holds all FSMO roles after provisioning. Leave them there,
especially the PDC emulator: the primary is the SYSVOL writer, and Group
Policy Management targets the PDC emulator by default.

## Validation status

| Area | Status | Tracking |
| --- | --- | --- |
| Secondary join, AD replication, DNS SRV records | Passed on the three-node lab | |
| SYSVOL synchronization (add, delete, drift overwrite, read-only shares) | Passed on the three-node lab | |
| Pod loss, host reboot, peer partition, Stork outage | Passed on the three-node lab | |
| Protocol checks from a Linux client (DNS, Kerberos, LDAP, SMB) | Passed against both controllers | |
| Interrupted SYSVOL transfer and restart | Not yet tested | [#38](https://github.com/GMouzourou/Domain-In-A-Box/issues/38) |
| Windows and Ubuntu domain-joined clients, Group Policy Management | Not yet tested | [#37](https://github.com/GMouzourou/Domain-In-A-Box/issues/37) |
| Storage failure and full-cluster cold restart | Not yet tested | [#39](https://github.com/GMouzourou/Domain-In-A-Box/issues/39) |
| Permanent controller loss, promotion and replacement | Not yet tested; promotion not yet implemented | [#40](https://github.com/GMouzourou/Domain-In-A-Box/issues/40) |
| Whole-domain backup and offline restore | Not yet defined | [#41](https://github.com/GMouzourou/Domain-In-A-Box/issues/41) |
| Physical power loss | Not yet tested | [#42](https://github.com/GMouzourou/Domain-In-A-Box/issues/42) |
| DHCP HA on Kubernetes (leases, takeover, duplicates) | Isolated Kea pair only | [#43](https://github.com/GMouzourou/Domain-In-A-Box/issues/43) |
| DNS server list for DHCP clients | Single server only | [#45](https://github.com/GMouzourou/Domain-In-A-Box/issues/45) |

Redundancy will be claimed only for the services and failures that pass these
tests. The chart will not offer a generic `replicas > 1` setting.
