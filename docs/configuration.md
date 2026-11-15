# Configuration reference

Docker deployments are configured with environment variables, usually in a
`.env` file next to `docker-compose.yml`. Kubernetes deployments are configured
with [Helm values](#helm-values); the chart sets the environment variables for
you.

Values marked **restart** are reconciled on every start. All other domain
settings are applied only when the domain is first provisioned. To change them
later, use the normal administration tools (`samba-tool`, Stork, RSAT) or start
again with empty storage. See
[Restarts and configuration changes](operations.md#restarts-and-configuration-changes).

## Environment variables

### Domain

| Variable | Default | Description |
| --- | --- | --- |
| `DIB_REALM` | `DOMAIN.HOME.ARPA` | Kerberos realm and DNS domain, in upper case. Choose a domain you own or a subdomain of `home.arpa`. |
| `DIB_DOMAIN` | realm's first label | NetBIOS (short) domain name, 15 characters or fewer. Empty uses the first label of `DIB_REALM`, upper-cased and truncated to 15 characters. Used only when the domain is first provisioned. |
| `DIB_DOMAIN_ADMIN_PASSWORD` | *required* | `Administrator` password used when the domain is provisioned. Must meet the AD complexity rules. Compose passes it as a secret file through `DIB_DOMAIN_ADMIN_PASSWORD_FILE`. |
| `DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART` | `false` | **Restart.** When `true`, resets `Administrator` to `DIB_DOMAIN_ADMIN_PASSWORD` on every start, overriding password changes made in AD. |
| `DIB_LDAP_PASSWORD` | *generated* (Compose) | Password of the service account Stork uses to check web UI logins against AD. Compose generates it once when empty. Required for the chart's credentials Secret. |

### Network, DNS and DHCP

| Variable | Default | Description |
| --- | --- | --- |
| `DIB_PARENT_INTERFACE` | `eth0` | Compose only. Host network interface used as the macvlan parent. |
| `DIB_LAN_SUBNET`, `DIB_LAN_GATEWAY` | `192.168.1.0/24`, `192.168.1.254` | Compose only. LAN subnet and gateway of the macvlan network. |
| `DIB_CONTROLLER_IP`, `DIB_STORK_IP` | `192.168.1.1`, `192.168.1.3` | Compose only. LAN addresses of the controller and the Stork web UI. `STORK_LDAP_URL` and `STORK_SERVER_HOST` default from them. |
| `DIB_INTERFACE` | `eth0` | Interface inside the controller container that carries the LAN address. Use `eth0` with Compose. |
| `DIB_REVERSE_ZONE` | *derived* | Reverse DNS zone created on first provisioning. Empty derives it from the LAN prefix: `/8`, `/16`, or the enclosing `/24` for `/24` and longer. Set it for other prefixes, for example `168.192.in-addr.arpa` for a `/23`. Changing it after provisioning does not move existing records. |
| `DIB_DNS_FORWARDERS` | `1.1.1.1; 8.8.8.8;` | **Restart.** Upstream DNS servers for names outside the domain, as a BIND list ending in `;`. |
| `DIB_DHCP_ENABLED` | `true` | Set `false` to disable the DHCP server, for example when your router keeps serving DHCP. |
| `DIB_DHCP_POOL` | `192.168.1.100-192.168.1.199` | **Restart.** DHCP address range. It must be inside the controller's subnet and must not include the controller, Stork, PostgreSQL or other static addresses. |
| `DIB_DHCP_DNS_SERVERS` | *empty* | **Restart.** Comma-separated IPv4 DNS servers advertised to DHCP clients. Empty advertises the controller itself, followed by its partner when DHCP HA is enabled. |

The DHCP subnet and router options are taken from the controller's own address
and default route.

DIB owns some settings in `/etc/kea/kea-dhcp4.conf`: the advertised DNS
servers, the dynamic DNS settings and, with DHCP HA, the peer list. They are
brought up to date on every start, so a changed variable or an upgrade reaches
an existing volume. Other edits to the file are kept. When DIB changes the
file, the previous version is saved as `kea-dhcp4.conf.dib-previous`.

### Monitoring

| Variable | Default | Description |
| --- | --- | --- |
| `DIB_SAMBA_METRICS_ENABLED` | `true` | **Restart.** Enables the Samba Prometheus exporter. |
| `DIB_SAMBA_METRICS_PORT` | `9922` | **Restart.** Samba exporter port. |
| `STORK_DATABASE_PASSWORD` | *generated* (Compose) | Password for Stork's PostgreSQL database. Compose generates it once when empty; set it for an external database. Required for the chart's credentials Secret. |
| `STORK_DATABASE_HOST`, `STORK_DATABASE_PORT`, `STORK_DATABASE_NAME`, `STORK_DATABASE_USER_NAME` | bundled PostgreSQL, `5432`, `stork`, `stork` | Stork database connection. |
| `DIB_STORK_DATABASE_EXTERNAL` | `false` | Set `true` when using your own PostgreSQL server. External connections require verified TLS. |
| `STORK_DATABASE_SSLMODE` | `disable` | PostgreSQL TLS mode. Use `verify-full` for an external database. |
| `STORK_DATABASE_SSLROOTCERT` | `/etc/stork/postgresql-tls/ca.crt` | CA certificate for verifying the database server. |
| `STORK_SERVER_HOST`, `STORK_SERVER_PORT`, `STORK_SERVER_SCHEME` | `DIB_STORK_IP`, `443`, `https` | Address the controller's Stork agent uses to register with the Stork server. |
| `STORK_LDAP_URL` | `ldaps://<DIB_CONTROLLER_IP>:636` | LDAPS URL the Stork server uses to check web UI logins against the controller. |
| `DIB_LDAPS_RELAY_ADDRESS` | unset; chart supplies pod IP | **Restart.** Relays only TCP port 636 on the pod IP to Samba's loopback LDAPS listener, without terminating TLS. Leave unset outside Kubernetes. If the pod IP is already the controller's LAN address, no relay is needed. |
| `DIB_BOOTSTRAP_TIMEOUT_SECONDS` | `120` | Maximum time, in seconds, for start-up dependency checks before bootstrap fails. |

### Certificates

| Variable | Default | Description |
| --- | --- | --- |
| `DIB_CONTROLLER_TLS_DIR` | `./certs/controller` | Host directory with the controller's `cert.pem`, `key.pem` and `ca.pem`. |
| `DIB_STORK_SERVER_TLS_DIR` | `./certs/stork-server` | Host directory with the Stork server's `cert.pem`, `key.pem` and `ca.crt`. |
| `DIB_POSTGRES_TLS_DIR` | `./certs/postgresql` | Host directory with the CA for an external PostgreSQL server. |

See [Certificates](certificates.md) for the required names and SANs.

### Insecure opt-ins

These weaken security and are intended only for isolated testing. Each defaults
to `false`.

| Variable | Effect |
| --- | --- |
| `DIB_ALLOW_INSECURE_STORK_HTTP` | Allows plain HTTP between the Stork agent and server. |
| `DIB_ALLOW_INSECURE_LDAP_TLS` | Allows Stork to skip LDAPS certificate verification. Also set `STORK_LDAP_SKIP_SERVER_VERIFICATION=true`. |
| `DIB_ALLOW_INSECURE_POSTGRESQL` | Allows an external PostgreSQL connection without certificate verification. |

## Helm values

Defaults are in
[`charts/domain-in-a-box/values.yaml`](../charts/domain-in-a-box/values.yaml).

### Container security and pod-network policy

| Value | Default | Description |
| --- | --- | --- |
| `security.privileged` | `false` | Compatibility escape hatch for the controller only. Normally uses explicit capabilities and RuntimeDefault seccomp. |
| `networkPolicy.enabled` | `false` | Enables release-scoped ingress/egress policy on the primary pod network; requires an enforcing CNI. |
| `networkPolicy.ingress` | `[]` | Additional Kubernetes ingress rules for clients, metrics and the Stork UI. |
| `networkPolicy.egress` | `[]` | Additional Kubernetes egress rules, including external PostgreSQL dependencies. |

See [container security](kubernetes.md#container-security) and
[network policy](kubernetes.md#optional-networkpolicy) before enabling these
settings. Standard NetworkPolicy does not protect the Multus/macvlan LAN.

### Images

| Value | Default | Description |
| --- | --- | --- |
| `coreImage.repository` | `ghcr.io/gmouzourou/domain-in-a-box` | Controller image. |
| `coreImage.tag` | same release as chart `appVersion` | Controller image tag. Release-pinned by default; may be overridden. |
| `storkImage.repository` | `ghcr.io/gmouzourou/domain-in-a-box-stork` | Stork server image. |
| `storkImage.tag` | same release as chart `appVersion` | Stork image tag. Release-pinned by default; may be overridden. |

### Domain

| Value | Default | Description |
| --- | --- | --- |
| `domain.realm` | `DOMAIN.HOME.ARPA` | As `DIB_REALM`. |
| `domain.name` | realm's first label | As `DIB_DOMAIN`. Empty uses the first label of `domain.realm`, upper-cased and truncated to 15 characters. |
| `domain.controllerMode` | `primary` | `primary` provisions a new domain; `secondary` joins an existing one. Cannot be changed once the volume holds data. |
| `domain.joinServer` | `peer.address` | Secondary only: override for the address of the controller to join. |
| `domain.interface` | `net1` | Interface inside the pod that carries the LAN address (the Multus attachment). |
| `domain.dhcpEnabled` | role-derived | As `DIB_DHCP_ENABLED`. Unset means `true` on a primary and `false` on a secondary; setting `true` on a secondary is rejected. With `dhcpHA.enabled`, the secondary still runs Kea as the standby. |
| `domain.dhcpPool` | `192.168.1.100-192.168.1.199` | As `DIB_DHCP_POOL`. |
| `domain.dhcpDNSServers` | `""` | As `DIB_DHCP_DNS_SERVERS`. |
| `domain.reverseZone` | `""` | As `DIB_REVERSE_ZONE`. |
| `domain.dnsForwarders` | `1.1.1.1; 8.8.8.8;` | As `DIB_DNS_FORWARDERS`. |
| `domain.syncPasswordOnRestart` | `false` | As `DIB_SYNC_DOMAIN_ADMIN_PASSWORD_ON_RESTART`. |

### Credentials

The chart reads passwords from an existing Secret; see
[Kubernetes setup](kubernetes.md).

| Value | Default |
| --- | --- |
| `credentials.secretName` | `domain-in-a-box-credentials` |
| `credentials.adminPasswordKey` | `domain-admin-password` |
| `credentials.databasePasswordKey` | `stork-db-password` |
| `credentials.ldapPasswordKey` | `stork-ldap-password` |

### Network

| Value | Default | Description |
| --- | --- | --- |
| `multus.enabled` | `true` | Attach the controller to the LAN through Multus. Required for clients to reach it. |
| `multus.createNAD` | `true` | Create the macvlan `NetworkAttachmentDefinition`. Set `false` to use one you manage. |
| `multus.networkName` | `<fullname>-macvlan` | Name of the `NetworkAttachmentDefinition`. |
| `multus.masterInterface` | `eth0` | Node interface used as the macvlan parent. |
| `multus.address` | `192.168.1.1/24` | Controller's static LAN address and prefix. |
| `multus.gateway` | `192.168.1.254` | LAN default gateway. |
| `multus.storkServerNetworkName` | `""` | Optional existing NetworkAttachmentDefinition giving the Stork server a LAN address. The chart does not create it. When set, the Stork Deployment uses the `Recreate` strategy so a static address is never held by two pods; upgrades briefly stop the Stork UI. |
| `clusterDNS.domain` | `cluster.local` | Cluster DNS domain that BIND forwards to the cluster DNS service. |

### Multi-controller

See [Multi-controller](multi-controller.md) for how these fit together.

| Value | Default | Description |
| --- | --- | --- |
| `peer.address` | `""` | LAN address of the other controller. Set it on both controllers; the join server, SYSVOL addresses and DHCP HA pair are derived from it and `multus.address`. |
| `peer.name` | `""` | Secondary only: the primary's resource name (its `fullnameOverride`). The Stork server Service and Stork TLS Secret names are derived from it. |
| `dhcpHA.enabled` | `false` | Enable the Kea hot-standby pair. |
| `dhcpHA.primaryAddress`, `dhcpHA.standbyAddress` | derived | **Restart.** Overrides for the LAN addresses of the two controllers. Empty uses `multus.address` and `peer.address` according to the role. To change them, see [Change a controller's DHCP HA address or name](operator-procedures.md#change-a-controllers-dhcp-ha-address-or-name). |
| `dhcpHA.primaryName`, `dhcpHA.standbyName` | `dib-primary`, `dib-secondary` | **Restart.** Kea peer names, used by `dhcp-ha` commands. |
| `sysvolSync.enabled` | `false` | Enable one-way SYSVOL synchronization. |
| `sysvolSync.secretName` | `""` | Secret with the SSH keys for this controller's role. |
| `sysvolSync.peerAddress` | `peer.address` | Primary only: override for the standby address allowed to pull. |
| `sysvolSync.writerAddress` | `peer.address` | Secondary only: override for the primary address to pull from. |
| `sysvolSync.sshPort` | `22` | SSH port on the writer. |
| `sysvolSync.intervalSeconds` | `60` | Pull interval. |

### Monitoring and database

| Value | Default | Description |
| --- | --- | --- |
| `metrics.enabled`, `metrics.port` | `true`, `9922` | Samba exporter. |
| `storkServer.enabled` | role-derived | Deploy the Stork server with this release. Unset means `true` on a primary and `false` on a secondary; setting `true` on a secondary is rejected. |
| `storkServer.host` | derived | Override for the Stork server address. Empty uses `<fullname>-stork-server` on a primary and `<peer.name>-stork-server` on a secondary. |
| `database.external.enabled` | `false` | Use an external PostgreSQL server instead of the bundled one. |
| `database.external.host`, `.port`, `.name`, `.user` | | External database connection. |
| `database.external.passwordSecret.name`, `.key` | `""`, `password` | Secret holding the database password. |
| `database.external.tls.sslMode` | `verify-full` | `verify-ca` or `verify-full`. `disable` and `require` also need `allowInsecure: true`. |
| `database.external.tls.rootCertSecretName` | `""` | Secret with the database CA. |
| `database.external.tls.clientCertSecretName` | `""` | Optional client certificate Secret. |

### TLS

| Value | Default | Description |
| --- | --- | --- |
| `tls.controller.secretName` | `<fullname>-controller-tls` | Controller certificate, key and CA. An explicit empty string disables controller TLS. |
| `tls.storkServer.enabled` | `true` | Serve the Stork UI and API over HTTPS. |
| `tls.storkServer.secretName` | `<stork owner>-stork-server-tls` | Stork server certificate and key. The owner is `<fullname>` on a primary and `peer.name` on a secondary. |
| `tls.storkServer.caSecretName` | `""` | CA the controller trusts for Stork. |
| `tls.storkServer.allowInsecureHttp` | `false` | Required to disable Stork HTTPS. |
| `tls.ldap.caSecretName` | controller Secret | CA Stork trusts for LDAPS. |
| `tls.ldap.serverName` | controller Service | LDAPS server name Stork verifies. |
| `tls.ldap.skipServerVerification`, `tls.ldap.allowInsecureSkipServerVerification` | `false`, `false` | Both required to skip LDAPS verification. |

### Storage and scheduling

| Value | Default | Description |
| --- | --- | --- |
| `persistence.enabled` | `true` | Store domain data on a PersistentVolumeClaim. Required for anything other than testing. |
| `persistence.storageClass` | cluster default | StorageClass for the data volume. |
| `persistence.size` | `10Gi` | Data volume size. |
| `resources` | 500m/1Gi requests, 2 CPU/4Gi limits | Controller resources. |
| `storkServer.resources`, `database.internal.resources` | 100m/256Mi requests, 1 CPU/1Gi limits | Stork and PostgreSQL resources. |
| `nodeSelector`, `tolerations`, `affinity` | empty | Pod scheduling. Multi-controller releases must pin to a node with `kubernetes.io/hostname`. |
