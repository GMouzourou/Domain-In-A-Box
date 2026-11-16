# Operations

## Monitoring

The controller exposes Prometheus metrics on its LAN address:

| Port | Exporter |
| --- | --- |
| `9119/tcp` | BIND DNS |
| `9547/tcp` | Kea DHCP |
| `9922/tcp` | Samba (`smb_prometheus_endpoint`), configurable with `DIB_SAMBA_METRICS_PORT` |

On Kubernetes, the same ports are on the controller Service.

The Stork web UI shows DHCP pools, leases, DNS zones and service health; see
[Docker](docker.md#3-start-the-stack) or
[Kubernetes](kubernetes.md#accessing-stork) for how to reach it. Stork is
monitoring only. If Stork or PostgreSQL is down, the domain, DNS and DHCP keep
working.

## Restarts and configuration changes

Domain data, DNS zones, DHCP leases and configuration files are stored on
persistent volumes and survive restarts. Most settings are applied only when
the domain is first provisioned. On every start, the controller reconciles
only:

- metrics settings (`DIB_SAMBA_METRICS_*`);
- the `Administrator` password, when `DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART`
  is `true`;
- the DNS forwarders (`DIB_DNS_FORWARDERS`);
- the DHCP pool (`DIB_DHCP_POOL`) of the generated subnet;
- the DHCP settings DIB manages in `kea-dhcp4.conf`: the DNS servers given to
  clients, dynamic DNS and, in a multi-controller deployment, the DHCP HA peer
  names and addresses. See
  [Network, DNS and DHCP](configuration.md#network-dns-and-dhcp).

Other changes you make to the configuration files on the volume are preserved.
Changing `DIB_REALM`, `DIB_DOMAIN`, the controller's address or, on
Kubernetes, `domain.controllerMode` after provisioning has no effect or is
refused. To change them, start again with empty storage.

To restart:

```bash
# Docker Compose
docker compose restart domain-controller

# Kubernetes
kubectl -n domain-in-a-box rollout restart statefulset/domain-in-a-box
```

A restart interrupts DNS, authentication and DHCP for the controller until it
is Ready again. Existing DHCP leases and Kerberos tickets keep working.

## Automatic recovery

supervisord restarts a crashed service inside the controller. A watchdog
stops the whole controller, so that Docker's restart policy or Kubernetes
starts it again, when a core service cannot be kept running:

- Samba, BIND, Chrony, Kea DHCPv4 or dynamic DNS reaches `FATAL` after
  repeated start failures; or
- one of them exits unexpectedly 5 times within 2 minutes.

Monitoring and synchronization programs (the Stork agent, the Samba metrics
exporter and SYSVOL synchronization) are excluded: their failures never
restart the controller. The log line starting `ERROR [watchdog]` names the
service and the reason.

On Docker, this relies on a restart policy such as `unless-stopped`, which the
supplied Compose file and examples use. A configuration error that stops a
service starting makes the controller restart repeatedly; Docker and Kubernetes
back off between attempts. Check the logs of the first failure, not the last.

The Kubernetes cluster DNS forwarders are retained across container restarts.
If an older controller has already lost its upstream resolver, recreate the
pod once (with its data volumes retained) to restore the platform resolver.

## Dynamic DNS

When a DHCP client sends its host name, the controller registers its `A` and
`PTR` records in AD DNS using a secure (GSS-TSIG) update, refreshes them on
every renewal and removes them when the lease is released or expires.

- Clients that register their own `A` record, such as domain-joined Windows
  computers, keep doing so; the controller only adds their `PTR` record.
- Each record set is paired with a `TXT` record containing a hash of the
  client's identity (its DHCID). The controller only changes records that carry
  its own marker for that client. Records you create, records an AD client
  registered itself and records belonging to another DHCP client are never
  overwritten or removed.
- Only single-label names in the AD domain and addresses in the controller's
  subnet are registered. The controller's own name and reserved names are
  refused.
- Failed updates are retried after 1, 5 and 15 seconds, then at the client's
  next renewal. Each change is logged with the prefix `[network-core]` and
  `DDNS register` or `DDNS remove`.

## Upgrades

1. Read the release notes for the new version.
2. [Back up](#backups) first.
3. Pin the new image tag; do not rely on `latest`.
4. Upgrade:

   ```bash
   # Docker Compose: update the image tags in docker-compose.yml, then
   docker compose pull
   docker compose up -d

   # Kubernetes
   helm upgrade domain-in-a-box ./charts/domain-in-a-box -n domain-in-a-box -f my-values.yaml
   ```

5. Check that the controller is Ready and the Stork UI shows all services
   healthy.

In a multi-controller deployment, upgrade one controller at a time, starting
with the secondary, and wait for replication and SYSVOL synchronization to be
healthy before upgrading the other.

### PostgreSQL dependency updates

The bundled database uses PostgreSQL 18 with a digest-pinned Alpine image.
Back up Stork's database before changing even a patch image or digest, and
check Stork login, agent registration and retained monitoring data afterward.
The refreshed image was checked with existing PostgreSQL 18 data and a logical
backup/restore; this does not certify a future major-version migration.

Changing the major image tag does not migrate an existing database. For a
future major upgrade, first confirm that Stork supports the target version,
stop Stork writes, and test PostgreSQL's supported upgrade or logical
dump/restore procedure on disposable copies. Keep the original volume and a
verified backup until the upgraded installation has passed its checks.
Rollback must restore the pre-upgrade database with the compatible old
Stork/PostgreSQL versions; do not mount upgraded database files with an older
major server.

### Docker Compose changes in 0.6.0

- The LAN addresses moved from `docker-compose.yml` to `.env`
  (`DIB_LAN_SUBNET`, `DIB_LAN_GATEWAY`, `DIB_CONTROLLER_IP`, `DIB_STORK_IP`).
  Copy any addresses you edited into `.env`.
- Keep `DIB_LDAP_PASSWORD` and `STORK_DATABASE_PASSWORD` in `.env`. The new
  `secrets` service writes them to the `dib-secrets` volume, and the existing
  database and LDAP account keep working. Only new installations can leave
  them empty.
- The bundled PostgreSQL volume is now mounted at `/var/lib/postgresql`, as
  PostgreSQL 18 requires. The previous mount stopped PostgreSQL 18 from
  starting, so no working database used it.

### Chart value changes in 0.6.0

Several chart values are now derived, so existing values files keep working
but can be shortened. Check these before upgrading:

- `multus.networkName` now defaults to `<fullname>-macvlan` instead of
  `macvlan-network`. With `multus.createNAD: true`, Helm replaces the
  attachment and the controller restarts on it. If you manage the attachment
  yourself (`createNAD: false`) and relied on the old default, set
  `multus.networkName: macvlan-network`.
- `tls.controller.secretName` and `tls.storkServer.secretName` now default to
  `<fullname>-controller-tls` and `<fullname>-stork-server-tls`. These match
  the old defaults for a release named `domain-in-a-box`; otherwise keep your
  explicit names.
- A secondary can replace `domain.joinServer`, `sysvolSync.writerAddress`,
  `storkServer.host`, `tls.storkServer.secretName` and the `dhcpHA` addresses
  with `peer.address` and `peer.name`, and the primary can replace
  `sysvolSync.peerAddress` and the `dhcpHA` addresses with `peer.address`. A
  secondary that set `storkServer.host` but relied on the old Stork TLS Secret
  default must now set `peer.name` or `tls.storkServer.secretName`.
- `domain.dhcpEnabled` and `storkServer.enabled` follow the controller role
  when unset, and `domain.name` follows the realm. Explicit values still win.
  The NetBIOS name is used only when the domain is first provisioned, so an
  existing domain is unaffected.

## Certificate rotation

Samba copies the controller certificate into its own state, and both Samba and
the Stork server read certificates only at start-up. Replacing the
certificate files or Secrets therefore has no effect until a restart.

1. Replace the certificate files ([Docker](certificates.md#docker-compose-file-layout))
   or Secrets ([Kubernetes](certificates.md#kubernetes-secrets)). Keep the same
   SANs.
2. In a maintenance window, restart the controller and the Stork server:

   ```bash
   # Docker Compose
   docker compose restart domain-controller stork-server

   # Kubernetes
   kubectl -n domain-in-a-box rollout restart statefulset/domain-in-a-box
   kubectl -n domain-in-a-box rollout restart deployment/domain-in-a-box-stork-server
   ```

3. Check that LDAPS and the Stork UI present the new certificate, for example
   with `openssl s_client -connect 192.168.1.1:636`.

If you rotate the CA, deploy the new CA to everything that trusts it (clients,
Stork and, in a multi-controller deployment, the other controller) before
switching certificates.

**Kubernetes releases first installed with a chart older than 0.3.0:** the Stork agent may
still be registered with its old LAN address. Remove the machine in the Stork
UI, delete `/var/lib/stork-agent/.stork-agent-registered` in the controller
pod, then restart the controller. It registers again using its stable Service
name.

## Backups

A supported backup and restore procedure is not yet defined. It is tracked in
[#41](https://github.com/GMouzourou/Domain-In-A-Box/issues/41). Until then:

- **Single controller:** stop the controller and copy all of its volumes
  ([Docker volumes](docker.md#volumes), or the `data` PersistentVolumeClaim on
  Kubernetes). Restore by stopping the controller and replacing all volumes
  together. Changes made after the backup are lost. This procedure has not
  been tested.
- **Multi-controller:** never restore one controller's volume into a running
  domain; see
  [Operator procedures](operator-procedures.md#never-restore-a-single-controller-from-a-backup).
  Replace a damaged controller by joining a new one.

Back up the certificates, `.env` file or Helm values and Kubernetes Secrets
separately. They are not stored on the controller's volumes. With Compose,
also back up the `dib-secrets` volume, which holds the generated Stork
passwords.

## Administration

Manage users, groups, computers and Group Policy with the Windows RSAT tools
from a domain-joined Windows computer, or with `samba-tool` inside the
controller:

```bash
docker exec -it domain-in-a-box samba-tool user list
kubectl -n domain-in-a-box exec -it domain-in-a-box-0 -- samba-tool user list
```

In a multi-controller deployment, make Group Policy edits on the primary;
see [Multi-controller](multi-controller.md).
