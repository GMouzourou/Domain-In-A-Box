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
- the DHCP pool (`DIB_DHCP_POOL`) of the generated subnet.

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
separately. They are not stored on the controller's volumes.

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
