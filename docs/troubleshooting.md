# Troubleshooting

## Collecting information

```bash
# Docker Compose
docker compose ps
docker compose logs --tail 200 domain-controller
docker compose logs --tail 200 stork-server

# Kubernetes
kubectl -n domain-in-a-box get pods,pvc
kubectl -n domain-in-a-box describe pod domain-in-a-box-0
kubectl -n domain-in-a-box logs domain-in-a-box-0 --tail 200
```

Most start-up failures are reported as a single clear message near the end of
the controller log.

## The host running Docker cannot reach the controller

This is expected with macvlan: the host cannot talk to its own macvlan
containers, but other machines on the LAN can. Test from another machine, or
add a host shim interface as described in
[Networking](networking.md#host-to-container-limitation).

## Nothing on the LAN can reach the controller

- Check that the controller's address is free and inside the LAN subnet, and
  that `DIB_PARENT_INTERFACE` (Compose) or `multus.masterInterface`
  (Kubernetes) is the host's LAN interface.
- Some switches, Wi-Fi access points and virtualization platforms drop frames
  from the extra MAC addresses that macvlan uses. Use a wired interface, or
  allow MAC changes / promiscuous mode on the virtual switch. See
  [Networking](networking.md#why-the-controller-needs-its-own-lan-address).
- Check that a firewall is not blocking the [required ports](networking.md#firewall-ports).

## `Failed to determine an IPv4 address for DIB_INTERFACE=...`

The interface named in `DIB_INTERFACE` (`domain.interface` on Kubernetes) does
not exist in the container or has no address.

- With Compose, `DIB_INTERFACE` is the interface **inside** the container,
  normally `eth0`. The host interface is set with `DIB_PARENT_INTERFACE`.
- On Kubernetes, the Multus attachment is normally `net1`. If it is missing,
  check that Multus is installed and the `NetworkAttachmentDefinition` exists:
  `kubectl -n domain-in-a-box get network-attachment-definitions`, and look for
  attachment errors in `kubectl describe pod`.

`Failed to determine subnet/gateway` means the interface has no default route;
check the gateway in your macvlan network or `multus.gateway`.

## `Controller mode changed from ... on existing data`

`domain.controllerMode` cannot be changed after a controller has been
provisioned or joined. Restore the original value, or start with a fresh data
volume if you really mean to build a different controller.

## `A previous secondary join may have left partial Samba state`

A secondary join started on this volume but did not complete. The controller
refuses to retry because a partial join can leave stale objects in the
domain. Check the domain for an entry left by the failed join and remove it
with `samba-tool domain demote --remove-other-dead-server=<name>` on the
primary. Then delete the secondary's PersistentVolumeClaim and install it
again. Rehearsal of this procedure is tracked in
[#40](https://github.com/GMouzourou/Domain-In-A-Box/issues/40).

## `Samba state exists ... but /etc/samba/smb.conf is missing`

The data volume is incomplete, typically because only some volumes were
restored or deleted. Restore all volumes together, or remove all of them to
provision a new domain.

## Certificate errors

- **Clients or Stork reject LDAPS:** the controller certificate must include
  the names and addresses clients use. Compare the certificate with
  [Required names](certificates.md#required-names):
  `openssl s_client -connect 192.168.1.1:636 </dev/null | openssl x509 -noout -text`.
- **`trusted CA certificate ... is empty` or cannot be read:** a CA file or
  Secret key is missing. Check the file names in
  [Certificates](certificates.md).
- **DHCP HA peers cannot connect:** each controller certificate must include
  its own LAN IP address and allow client authentication, because the peers
  use mutual TLS.
- **Certificate changes have no effect:** certificates are read at start-up.
  See [Certificate rotation](operations.md#certificate-rotation).

## Stork shows no machines or cannot log in

- The agent registers with the Stork server when the controller starts. Check
  the controller logs for registration errors and the Stork server logs for
  database or LDAP errors.
- `Stork agent callback address changed`: the agent was registered under a
  different address. Remove the machine in the Stork UI, delete
  `/var/lib/stork-agent/.stork-agent-registered` in the controller, and
  restart it.
- Logins use Active Directory over LDAPS. Check that the user is in Domain
  Users or Domain Admins, and that Stork trusts the controller's CA.

Stork problems do not affect domain, DNS or DHCP service.

## DHCP clients receive no address

- Check that `DIB_DHCP_ENABLED` is `true` and that no other DHCP server (for
  example your router) is active on the LAN.
- Check that `DIB_DHCP_POOL` is inside the controller's subnet.
- In a multi-controller deployment with the primary down, the standby does not
  serve DHCP until an operator
  [takes over](operator-procedures.md#take-over-dhcp-on-the-secondary).

## Multi-controller checks

```bash
kubectl -n dib exec dib-primary-0 -- samba-tool drs showrepl
kubectl -n dib exec dib-secondary-0 -- samba-tool ntacl sysvolcheck
kubectl -n dib logs dib-secondary-0 | grep -i sysvol
```

Writes to SYSVOL on the secondary fail with `NT_STATUS_ACCESS_DENIED` by
design; make Group Policy changes on the primary.
