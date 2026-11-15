# Multi-controller design notes

This page records why the [multi-controller mode](multi-controller.md) works
the way it does and what has been tested. It replaces the former
`REDUNDANCY-DESIGN.md` (issue
[#32](https://github.com/GMouzourou/Domain-In-A-Box/issues/32)).

## Goal

After one physical host is lost, keep as many domain services running as
possible without asking the operator to do anything complicated. Where a
service cannot continue safely without knowing that the failed controller has
really stopped, it degrades predictably instead of guessing.

The target is best-effort recovery within five minutes for automatic
services, on Windows and Ubuntu clients. This is not a guarantee: client
resolver, DC locator and SMB timeouts also affect what users see.

## Principle: no fencing

Domain-In-A-Box users are not expected to have a fence: an independent device,
such as a switched power outlet or management controller, that proves a host
is off. Kubernetes Leases, pod deletion and node `NotReady` do not prove that
a process has stopped; a partitioned host may still be serving clients. The
design therefore never depends on a fence:

- **Two pinned controllers.** Each controller has its own identity, address,
  data volume and physical host. A controller is never rescheduled to another
  host or started on a copy of another's data.
- **Prefer partition-safe services.** AD, DNS and Kerberos are multi-master.
  Both controllers keep accepting reads and writes during a partition, and
  replication converges afterwards.
- **Fixed authority for single-writer services.** SYSVOL and DHCP lease
  allocation each have one designated owner. Authority never moves
  automatically, because an unreachable owner might still be running.
- **One simple manual step where needed.** Moving authority is a deliberate
  [operator procedure](operator-procedures.md) with one precondition: the
  operator has confirmed the other controller is off or permanently gone.

## Decisions

1. **Topology.** Two controllers on separate physical hosts. The chart stays
   at one replica; the second controller is a separate, explicitly joined
   release, not `replicas: 2`.
2. **Availability over unreplicated changes.** A surviving controller keeps
   accepting AD writes. Changes acknowledged only by a controller that is
   destroyed before replicating are lost. There is no quantitative
   data-loss bound.
3. **SYSVOL: fixed writer, one-way synchronization.** The primary, which also
   holds the PDC emulator role, is the only SYSVOL source. The secondary's
   SYSVOL and NETLOGON shares are read-only on every start, even when the
   primary is healthy. There is no reachability-based role switch.
4. **DHCP: Kea hot-standby with manual takeover.** `auto-failover` is `false`.
   The standby keeps a synchronized lease copy but does not serve clients
   until an operator enables it.
5. **Permanent primary loss: one documented procedure.** Seize FSMO roles,
   remove the dead controller, promote the survivor to SYSVOL writer. The
   promotion step is not yet implemented
   ([#40](https://github.com/GMouzourou/Domain-In-A-Box/issues/40)).
6. **Power-loss testing is a test, not a recovery mechanism.**
7. **Trust boundary.** Node root users and Kubernetes cluster administrators
   are trusted.

## SYSVOL synchronization

Samba does not implement FRS or DFS-R SYSVOL replication. It documents one-way
[rsync from a designated source](https://wiki.samba.org/index.php/Rsync_based_SysVol_replication_workaround)
as a workaround, and warns that edits made on other controllers are
overwritten. A Group Policy Object consists of an AD container, replicated
through AD, and a SYSVOL template, synchronized separately
([Microsoft](https://learn.microsoft.com/en-us/windows-server/identity/ad-ds/manage/group-policy/group-policy-overview)).
This is why Group Policy edits must target the primary.

**ID mapping.** In testing, the same numeric UIDs and GIDs mapped to different
SIDs on the two controllers. For example, `3000004` was Domain Admins on one
and Domain Guests on the other. Copying ownership and POSIX ACLs numerically
would grant the wrong principals access. The implementation therefore does
not transfer ownership, POSIX ACLs, permissions or extended attributes.

**Process:**

1. The writer builds a sanitized, read-only export and a manifest of hashes.
2. The standby pulls it over SSH, using a dedicated key-only account
   restricted to `rrsync -ro` and a pinned writer host key.
3. The standby checks the received tree against the manifest, rebuilds local
   ACLs with `samba-tool ntacl sysvolreset`, and checks the staged tree with
   `sysvolcheck`.
4. Only then does it atomically swap the staged tree with the live one. A
   persisted pending manifest lets the next run recover from a crash during
   the swap. Failed transfers leave the last published tree in place.
5. The writer compares AD GPO versions with `GPT.INI`. The standby logs these
   mismatches, and any local changes, before overwriting them.

The read-only shares block SMB file edits on the standby. They do not prevent
separate AD writes, and they do not make a Group Policy Management operation
transactional.

## Kea DHCP HA

The pinned Kea 3.0.3 packages include the HA and lease-commands hooks. Per the
[Kea HA documentation](https://kea.readthedocs.io/en/kea-3.0.3/arm/hooks.html#hooks-high-availability):

- In `hot-standby`, only the primary answers DHCP queries; the standby receives
  lease updates.
- `max-unacked-clients: 0` lets Kea report `partner-down` after heartbeat loss
  without waiting for client traffic. With `auto-failover: false`, that state
  does not grant the standby the primary's scope. A partition can also cause
  `partner-down`, so the operator must confirm the primary is off.
- Manual takeover uses Kea's `ha-scopes` command, wrapped by
  `dib-network-core-ctl dhcp-ha takeover` and `resume`. The wrapper requires
  explicit confirmation and a local `partner-down` state. It does not gate on
  Kea's `unsent-update-count`, which is cumulative and describes the standby's
  own unsent updates rather than the primary's
  ([#51](https://github.com/GMouzourou/Domain-In-A-Box/issues/51)). `ha-scopes` takes the peer name
  (for example `dib-primary`), not the `HA_<name>` client class.
- Peers communicate over Kea's dedicated HTTPS listener on port `8005`, with
  mutual TLS using the controller certificates. Each certificate must include
  the controller's LAN IP address.
- Each controller has its own GSS-TSIG account (`kea-<hostname>`) and keytab
  for DDNS. The accounts are members of `DnsAdmins`, so after a takeover the
  secondary can renew and remove records the primary registered. Ownership is
  tracked by a TXT marker carrying the client's DHCID, not by the account
  that created the record.

## Secondary join

A secondary joins with `samba-tool domain join <realm> DC`, using the
existing controller as its resolver during the join
([Samba](https://wiki.samba.org/index.php/Joining_a_Samba_DC_to_an_Existing_Active_Directory)).
The join does not populate SYSVOL; synchronization does that. Samba's KCC can
take up to 15 minutes to create replication agreements; this is initial-join
time, not failover time.

The generated `smb.conf` defaulted to a lower functional level than the domain
(`2008_R2` versus `2016`) and refused to start. The join now writes and checks
the managed `smb.conf` settings, including the functional level.

A persistent join-started marker makes the secondary refuse to retry a failed
or partial join on the same volume. The controller mode is also recorded on
the volume, and a mode change on existing data is refused.

## Validation evidence

### Three-node RKE2 lab

Primary on node1 and secondary on node2, with node3 for test clients. DHCP was
disabled throughout.

- Both controllers joined and became Ready. AD replication covered all five
  naming contexts; `sysvolcheck` passed on both and SYSVOL file digests
  matched.
- **Primary pod loss:** the primary's endpoints became unavailable while the
  secondary stayed reachable, with read-only shares and a passing SYSVOL
  check. The pod returned Ready in 22 seconds, and replication and SYSVOL
  synchronization recovered.
- **Host reboots:** rebooting either controller's host left the other
  reachable. The rebooted controller was Ready again about 4 minutes 21
  seconds (primary) and 4 minutes 26 seconds (secondary) after the reboot was
  scheduled. Replication converged automatically.
- **Peer partition:** a two-minute controller-to-controller partition left
  both controllers reachable by clients. SYSVOL synchronization logged
  failures and kept the last published tree. After the partition, SYSVOL
  checks passed and replication recovered in under five minutes.
- **Stork outage:** with the Stork server scaled to zero, both controllers
  stayed Ready and reachable.
- **Linux client protocol checks:** an Ubuntu 24.04 client resolved both
  controllers through SRV records and made Kerberos-authenticated LDAP binds
  and SYSVOL reads against both. Writes to the secondary's SYSVOL and NETLOGON
  returned `NT_STATUS_ACCESS_DENIED`. Files added on the primary appeared on
  the secondary, deletions propagated, and a secondary-only file was logged
  as drift and overwritten.
- **Time:** all hosts had healthy Chrony tracking, with offsets of about
  0.17 ms or less at the time of testing.

These were reboots, not power cuts. The client did not join the domain, so the
results do not cover SSSD/PAM logins, Windows Group Policy Management or full
client failover.

### Isolated Kea HA pair

Two containers established the mutual-TLS peer relationship, reached
hot-standby, moved the standby to `partner-down` when the primary's Kea
stopped, and exercised manual `takeover` and `resume`. The primary returned to
hot-standby after restart. No DHCP clients or leases were involved.

### Monitoring independence

In a single-node kind cluster with PostgreSQL and Stork scaled to zero, a new
controller became Ready, served a DHCP lease, and passed DNS, LDAP and SMB
checks, including after a restart. When monitoring was restored, registration
and exporters recovered and the full test suite passed.

## Remaining work

Outstanding validation and features are tracked as issues; see
[Validation status](multi-controller.md#validation-status).
