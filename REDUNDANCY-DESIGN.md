# Multi-Controller Redundancy Design

**Status: investigation and design review.** Multi-controller operation is not
implemented or supported. Do not scale the current StatefulSet or treat this
document as a deployment recipe.

## Goal and boundaries

The proposed goal is service recovery after a single physical-host failure
where safe automatic recovery can be demonstrated. This does not imply zero
interruption, automatic recreation of a failed controller, or automatic
takeover during an ambiguous network partition. If peers cannot distinguish a
failed controller from a partition, preserve directory and lease safety, alert,
and require operator action rather than risk split brain.

The intended client platforms are Windows and Ubuntu Linux. A surviving
controller must continue to provide the specifically claimed services,
including access to already-published Group Policy and logon scripts. Recovery
deadlines and acceptable data loss must be measured in the prototype and
agreed before any release claim.

The lab described by the proposal has at least three physical hosts, an
embedded-etcd RKE2 control plane with at least three server nodes, and
Longhorn v1.13.0. Host placement, Kubernetes/RKE2 compatibility, Longhorn
engine, storage topology, and recovery targets are not yet selected. Longhorn
v1.13.0 requires Kubernetes v1.34 or later; verify the exact RKE2 version and
host prerequisites before adopting it.

## Current constraints

- The chart deploys one StatefulSet replica and one persistent data volume.
  Bootstrap provisions a domain; there is no distinct secondary-controller
  join lifecycle.
- Samba's domain database, BIND configuration, Kea lease file, DDNS keytab,
  controller address, and local service bindings are designed for one
  controller. Separate controller identities and data volumes are mandatory;
  sharing a Samba volume is not replication.
- Kea uses a local memfile lease database and targets local DDNS services.
  Neither the current lease storage nor its DDNS credentials coordinate peers.
- BIND forwards the AD domain and reverse zone to local Samba DNS. DNS,
  Kerberos, DHCP, and NTP client failover behavior is not established.
- Monitoring and PostgreSQL are not domain data-plane services. Their failure
  must not prevent fresh core bootstrap or readiness once single-controller
  monitoring support is claimed.

## Review findings and required design decisions

### Failure domains and storage

Separate pods do not guarantee separate physical-host or storage failure
domains. Controllers in separate releases need required placement rules on a
domain-wide node/zone label. State what happens when there are too few eligible
nodes. Define the shared power, network, and storage dependencies.

`ReadWriteOnce` does not guarantee safe or immediate reattachment after host
loss. Specify whether the failed controller is recovered from its volume or
replaced and rejoined. If Longhorn is used, select its engine, replica count,
physical placement, anti-affinity, disk headroom, and node-down pod-deletion
policy. With three eligible storage hosts and strict replica anti-affinity,
there is no fourth host to restore all replicas after one host fails. Volume
replicas are not additional domain controllers, and `strict-local` is not
volume redundancy.

Replicas and local snapshots are not independent backups. Define application-
aware Samba backups, separate Longhorn backups on an independent target, RKE2
etcd recovery, retention, recovery-point objectives, and a cold-start drill
that does not depend on unavailable domain-controller DNS or time.

### Network reachability

The proposed macvlan path must be tested from every physical host and LAN
client. Linux macvlan host/parent isolation can prevent a host from reaching a
local macvlan child, including when a peer failure leaves only the local DC
available. Define and test host-to-local-DC and cross-node paths, DHCP
broadcast or relay behavior, switch/virtual-NIC MAC policy, static address
allocation, and address recovery. Do not infer cross-node failure from the
host-isolation case alone.

### Kea, lease safety, and DDNS

Choose a Kea HA mode and an explicit safety-versus-availability policy for
peer-only partitions. A takeover that is safe only when a peer is known dead
needs a fencing or operator-intervention mechanism. Hot-standby detection can
depend on client traffic and DHCP `SECS` behavior, so exercise a single client,
low traffic, unicast renewals, broadcasts, peer/client-path partitions,
rejoin, and lease synchronization. Assert that there are no duplicate
allocations.

Verify secure forward and reverse DDNS updates while either controller is
unavailable, including interruption and reconciliation after failover. Record
the exact image digest, package versions, and available Kea hooks; the image
currently does not pin APT package versions.

### AD replication and SYSVOL

Prototype a separate, idempotent secondary-DC join lifecycle using distinct
identity and storage. Validate bidirectional AD replication, DNS records,
LDAP/Kerberos authentication, restarts, stale-join recovery, and supported
replacement behavior.

Samba does not provide native FRS/DFS-R SYSVOL replication. Select and test a
separate synchronization method, including ACLs, ID mapping, write ownership,
outage, and reintegration. A release gate must require a real Windows and
Ubuntu client to read and apply an already-published policy and logon script
when either DC is unavailable. Define whether policy edits are allowed while
the designated writer is unavailable.

### Recovery, fencing, and time

Distinguish a healthy-domain controller replacement from catastrophic domain
restore. Define fencing before any forced pod deletion or replacement; a
possibly partitioned controller must not reappear with the same identity.
Document FSMO ownership, temporary role loss, transfer versus seizure, backup
scope, credentials/keytabs, lease-state recovery, and isolated restore drills.

Kea must advertise the intended DNS resolvers to clients, and existing DHCP
leases and statically configured clients need a migration plan. Test Windows
and Ubuntu discovery and login with either resolver unavailable.

Chrony runs with `-x`, which does not adjust the host clock. NTP responses
alone therefore do not prove a DC's system clock is suitable for Kerberos.
Specify independent host time synchronization and PDC-emulator/time-hierarchy
behavior, then test offsets and a fresh Kerberos exchange after failover and
cold start.

### Monitoring independence and measurable gates

The current first-time bootstrap waits for PostgreSQL before core bootstrap,
and StatefulSet readiness checks observability ports. Separate core readiness
and recovery from Stork/PostgreSQL availability before claiming monitoring is
optional. Test fresh startup, restart, and recovery while those dependencies
are unavailable.

Replace generic "verify" criteria with a failure matrix that names the
service, Windows/Ubuntu client path, recovery mode, allowed interruption, and
correctness assertion. Include process/host loss, peer-only partition,
client-path failure, monitoring loss, storage failure, and full-lab restart.
The existing kind test is single-node and cannot establish RKE2 quorum,
physical-host fault tolerance, or real-LAN behavior.

## Staged work

1. Agree on service availability goals, failure domains, client platforms,
   recovery deadlines, acceptable data loss, and safe behavior during
   ambiguous partitions.
2. Select physical placement, RKE2 control-plane topology, Longhorn
   compatibility and placement, node-failure policy, LAN/macvlan strategy,
   independent host DNS/time, and a cold-start sequence.
3. In an isolated environment, implement and validate explicit primary
   provisioning and secondary-DC joining with distinct data, replicated AD/DNS,
   SYSVOL synchronization, surviving-client policy/logon-script access, and
   idempotent restart behavior.
4. Prototype the selected Kea HA and secure DDNS configuration with the exact
   image and the low-traffic, partition, lease-ownership, and rejoin tests.
5. Separate core bootstrap/readiness from Stork/PostgreSQL and test monitoring
   outages and recovery.
6. Document and exercise single-DC replacement, FSMO handling, catastrophic
   domain restore, independent Longhorn/RKE2 backups, and safe fencing.
7. Run the complete failure matrix on representative multi-node RKE2 hardware.
   Record versions and results before revising release or support claims.

## Decisions still open

- RKE2 server placement and compatible Kubernetes version.
- Longhorn data engine, replica placement, disk capacity, node-down handling,
  and independent backup target.
- Policy-editing behavior during an outage and DNS/NTP migration for existing
  clients.
- Per-service recovery-time and data-loss targets, and a tested way to decide
  whether automatic takeover is safe.
- Final controller packaging and Kea HA modes. No mode is assumed before the
  prototype.

## Release gate

Do not expose a generic `replicas > 1` setting. Claim redundancy only for the
services and failure cases that pass the prototype and multi-host failure
matrix. Document services that remain singleton, require operator action, or
have not been validated.
