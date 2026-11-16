# Operator procedures

These procedures apply to [multi-controller deployments](multi-controller.md).
They are the only steps where the operator must be certain about the state of
the other controller.

Each procedure has one precondition. **If you cannot confirm it, wait for the
failed host to return.** Waiting is always safe; acting on a wrong assumption
can cause duplicate DHCP addresses, two SYSVOL writers or two FSMO holders.

A failed heartbeat, a node shown as `NotReady`, a deleted pod or an expired
Kubernetes Lease does **not** prove that a controller has stopped. Confirm it
physically or through your hypervisor or power controller.

The examples use namespace `dib`, primary pod `dib-primary-0` and secondary
pod `dib-secondary-0`.

## Take over DHCP on the secondary

Use this when the primary is down for longer than DHCP clients can tolerate.
Existing leases keep working until they expire.

**Precondition:** the primary host is confirmed powered off or permanently
lost, and will stay off until DHCP authority has been returned.

1. Check the secondary's state:

   ```bash
   kubectl -n dib exec dib-secondary-0 -- dib-network-core-ctl dhcp-ha status
   ```

   Wait until `state` is `partner-down`, and check that the primary's peer
   name (default `dib-primary`) is not listed in `scopes`.

2. Consider which leases the secondary may not know about. In hot-standby the
   primary waits for the secondary to acknowledge each lease before answering
   the client, so normally nothing is lost. The exception is a period when the
   secondary was itself unreachable or restarting while the primary kept
   serving: leases the primary issued then, and had not resynchronized before
   it failed, are unknown to the secondary, which could offer one of those
   addresses again. If that applies, prefer waiting for the primary.

   The status output's `unsent-update-count` is not a guide here. Kea counts it
   from process start and never resets it, and it records updates the
   *secondary* failed to send, so `takeover` no longer checks it. The old
   `--accept-unsent-updates` flag is accepted but has no effect.

3. Take over:

   ```bash
   kubectl -n dib exec dib-secondary-0 -- \
     dib-network-core-ctl dhcp-ha takeover --confirm-primary-off
   ```

4. Confirm the command succeeded and that `status` now lists the primary's
   peer name in `scopes`. Keep the primary host off.

### Return DHCP to the primary

**Precondition:** the primary host is still off, or its Kea DHCP service is
stopped.

1. Withdraw the secondary's authority. This deliberately stops DHCP service
   until the primary is back:

   ```bash
   kubectl -n dib exec dib-secondary-0 -- \
     dib-network-core-ctl dhcp-ha resume --confirm-primary-dhcp-stopped
   ```

2. Only after the command succeeds, start the primary.
3. Confirm both controllers report the normal `hot-standby` state and that
   lease synchronization has completed.

If synchronization does not complete, leave the secondary's scope withdrawn
and investigate. Never enable DHCP on both controllers.

## Permanent loss of the primary

Use this only when the primary will **never** return with its existing data,
not for an outage that is expected to end.

**Precondition:** the primary's host is wiped, or will never be powered on
with its existing data volume.

> **Current limitation.** The secondary cannot yet be converted into the
> SYSVOL writer. Its SYSVOL and NETLOGON shares stay read-only, so Group
> Policy cannot be edited after the primary is permanently lost. Logons,
> authentication, DNS and the last synchronized Group Policy continue to
> work. A guarded promotion path and its rehearsal are tracked in
> [#40](https://github.com/GMouzourou/Domain-In-A-Box/issues/40).

What you can do now:

1. Seize the FSMO roles on the secondary:

   ```bash
   kubectl -n dib exec -it dib-secondary-0 -- samba-tool fsmo seize --role=all
   ```

2. Remove the dead controller from the directory:

   ```bash
   kubectl -n dib exec -it dib-secondary-0 -- \
     samba-tool domain demote --remove-other-dead-server=<primary-hostname> \
     -U Administrator
   ```

3. If DHCP HA was enabled, take over DHCP as described above.

Policy edits on the old primary that had not been synchronized are lost.

## Replace a permanently failed secondary

**Precondition:** the failed secondary's host is wiped, or will never return
with its existing data.

1. Remove the dead controller from the directory, from the primary:

   ```bash
   kubectl -n dib exec -it dib-primary-0 -- \
     samba-tool domain demote --remove-other-dead-server=<secondary-hostname> \
     -U Administrator
   ```

2. Uninstall the old release and delete its PersistentVolumeClaim. **Never
   reuse the old controller's volume.**
3. Install a new secondary with empty storage, as in
   [Multi-controller setup](multi-controller.md#4-install-the-secondary).
   Using a new controller name avoids confusion with the removed one.

## Change a controller's DHCP HA address or name

A controller's own LAN address is fixed when it is provisioned: its
certificates, DNS records and Samba configuration use it. To move the
secondary, [rebuild it](#replace-a-permanently-failed-secondary) at the new
address (with a certificate for that address), then use this procedure so the
primary uses the new peer address.

DHCP HA peer names and addresses are reconciled on every start (see
[Network, DNS and DHCP](configuration.md#network-dns-and-dhcp)), so the change
is made through the Helm values, not by editing `kea-dhcp4.conf`.

1. Update `multus.address` and `peer.address` (or the `dhcpHA.primaryAddress`,
   `dhcpHA.standbyAddress` overrides, or the peer names) in the values of
   **both** releases. Both controllers must list the same
   peers.
2. Upgrade both releases. Each controller rewrites its HA peer list and saves
   the previous file as `kea-dhcp4.conf.dib-previous`.
3. Check that both controllers report HA state `hot-standby` and that each
   reaches its partner:

   ```bash
   kubectl -n dib exec dib-primary-0 -- dib-network-core-ctl dhcp-ha status
   kubectl -n dib exec dib-secondary-0 -- dib-network-core-ctl dhcp-ha status
   ```

The pair cannot synchronize until both controllers are upgraded, so upgrade
them one after the other and check the status straight away. A controller refuses to start if the change
would swap its role (its persisted name becomes its partner's name); roles are
fixed when a controller is provisioned.

## Never restore a single controller from a backup

Do not restore or revert one controller's volume from a snapshot or backup
while the domain is running. AD detects this as USN rollback, and the
restored controller silently stops replicating correctly. Always replace a
damaged controller by joining a new one.

Whole-domain restore (all controllers down, restored together) is a separate
offline disaster-recovery procedure. It is not yet defined; see
[#41](https://github.com/GMouzourou/Domain-In-A-Box/issues/41).
