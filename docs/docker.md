# Docker

Docker runs a single domain controller on one host. Choose:

- **Docker Compose** for the full stack: the controller, the Stork monitoring
  server and a bundled PostgreSQL database.
- **Docker CLI** for the core services only (AD, DNS, DHCP and NTP), without
  monitoring.

Both use a macvlan network so the controller has its own LAN address. Compose
needs Docker Engine 28.0 or later and Docker Compose 2.36 or later, which keep
the controller's LAN interface named `eth0` across restarts. Read
[Networking](networking.md) first, in particular the
[host-to-container limitation](networking.md#host-to-container-limitation).

## Docker Compose

### 1. Prepare the configuration

```bash
git clone https://github.com/GMouzourou/Domain-In-A-Box.git
cd Domain-In-A-Box
cp .env.example .env
```

Edit `.env`:

- Set `DIB_REALM` (for example `AD.HOME.ARPA`). The NetBIOS name
  `DIB_DOMAIN` defaults to the realm's first label (`AD`).
- Set `DIB_DOMAIN_ADMIN_PASSWORD`. Compose passes it to the controller as a
  secret file, so it does not appear in the container's environment.
- Set `DIB_PARENT_INTERFACE` to the host interface on your LAN (for example
  `eth0` or `enp3s0`). Leave `DIB_INTERFACE` as `eth0`, the interface name
  inside the container.
- Set the LAN addresses:

  | Variable | Default | Meaning |
  | --- | --- | --- |
  | `DIB_LAN_SUBNET` | `192.168.1.0/24` | LAN subnet |
  | `DIB_LAN_GATEWAY` | `192.168.1.254` | LAN gateway |
  | `DIB_CONTROLLER_IP` | `192.168.1.1` | Controller address |
  | `DIB_STORK_IP` | `192.168.1.3` | Stork web UI address |

  Issue the certificates for these addresses.
- Set `DIB_DHCP_POOL` to a free range and `DIB_DNS_FORWARDERS` to your upstream
  resolvers.

The Stork LDAP and database passwords are generated on first start by the
one-shot `secrets` service and kept in the `dib-secrets` volume. Set
`DIB_LDAP_PASSWORD` or `STORK_DATABASE_PASSWORD` only if you want your own
values, for example for an external database.

Compose runs the controller as root with an explicit capability profile, not
`privileged`. See [container security](kubernetes.md#container-security) for its
limits. Set `DIB_PRIVILEGED=true` only as a deliberate compatibility escape hatch
after investigating an unsupported host/runtime; this grants substantially
broader host access. It is a Compose setting, not a bootstrap environment variable.

The bundled PostgreSQL service has no LAN address. It is attached only to the
internal `stork_database_net` bridge network (`172.31.255.0/29`, database at
`172.31.255.2`), which the controller and the Stork server also join, so LAN
clients cannot reach the database. If that range overlaps a network the
controller must reach, change the subnet, the PostgreSQL address and
`STORK_DATABASE_HOST` together in `docker-compose.yml`.

### 2. Add certificates

Create `./certs/controller` and `./certs/stork-server` as described in
[Certificates](certificates.md#creating-a-private-ca-and-certificates).

### 3. Start the stack

```bash
docker compose pull
docker compose up -d
docker compose logs -f domain-controller
```

On first start, the controller provisions the domain. This takes a few
minutes. It is ready when `docker compose ps` reports it running and DNS
answers:

```bash
dig @192.168.1.1 _ldap._tcp.ad.home.arpa SRV
```

Run that check from another machine, or set up the host shim first.

The Stork UI is at `https://192.168.1.3/`. Sign in with an Active Directory
account: members of Domain Admins get administrator access, and other Domain
Users get read-only access.

### Volumes

| Volume | Path | Contents |
| --- | --- | --- |
| `samba-data` | `/var/lib/samba` | AD database, SYSVOL, Samba configuration (`/etc/samba` is linked here) |
| `bind-config` | `/etc/bind` | BIND configuration |
| `bind-data` | `/var/cache/bind` | BIND working data |
| `kea-config` | `/etc/kea` | Kea configuration |
| `kea-data` | `/var/lib/kea` | DHCP lease database |
| `chrony-config` | `/etc/chrony` | Chrony configuration |
| `chrony-data` | `/var/lib/chrony` | Chrony drift data |
| `stork-config` | `/etc/stork` | Stork agent configuration |
| `stork-data` | `/var/lib/stork-agent` | Stork agent certificates, key, token and registration marker |
| `log-data` | `/var/log` | Service logs |
| `postgres-data` | `/var/lib/postgresql` | Stork database |
| `dib-secrets` | `/run/dib-secrets` | Generated Stork LDAP and database passwords |

Replace any named volume with a host path (for example
`/srv/dib/samba:/var/lib/samba`) if you prefer direct access.

### Managing the stack

```bash
docker compose ps                 # status
docker compose logs -f            # logs
docker compose restart            # restart, keeping data
docker compose down               # stop and remove containers, keeping volumes
docker compose exec domain-controller bash
```

Do not run `docker compose down -v` unless you intend to delete the domain.

To use an existing PostgreSQL server instead of the bundled one, set
`DIB_STORK_DATABASE_EXTERNAL=true` and the `STORK_DATABASE_*` variables in
`.env`, place its CA at `./certs/postgresql/ca.crt`, and start only the
controller and Stork:

```bash
docker compose up -d domain-controller stork-server
```

## Docker CLI (core services only)

### 1. Create the macvlan network

```bash
docker network create -d macvlan \
  --subnet=192.168.1.0/24 \
  --gateway=192.168.1.254 \
  -o parent=eth0 \
  domain_net
```

### 2. Run the controller

```bash
: "${DIB_DOMAIN_ADMIN_PASSWORD:?Set DIB_DOMAIN_ADMIN_PASSWORD}"
docker run -d \
  --name domain-in-a-box \
  --restart unless-stopped \
  --cap-drop ALL \
  --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add DAC_READ_SEARCH \
  --cap-add FOWNER --cap-add FSETID --cap-add SETGID --cap-add SETUID \
  --cap-add SETPCAP --cap-add NET_BIND_SERVICE --cap-add NET_ADMIN \
  --cap-add NET_RAW --cap-add SYS_CHROOT --cap-add SYS_PTRACE \
  --cap-add SYS_ADMIN --cap-add SETFCAP --cap-add KILL \
  --network domain_net \
  --ip 192.168.1.1 \
  --hostname domain-server \
  -e DIB_REALM="AD.HOME.ARPA" \
  -e DIB_DOMAIN_ADMIN_PASSWORD \
  -e DIB_INTERFACE="eth0" \
  -e DIB_DHCP_POOL="192.168.1.100-192.168.1.199" \
  -e DIB_DNS_FORWARDERS="1.1.1.1; 8.8.8.8;" \
  -v samba-data:/var/lib/samba \
  -v bind-config:/etc/bind \
  -v bind-data:/var/cache/bind \
  -v kea-config:/etc/kea \
  -v kea-data:/var/lib/kea \
  -v chrony-config:/etc/chrony \
  -v chrony-data:/var/lib/chrony \
  -v log-data:/var/log \
  -v "$PWD/certs/controller:/certs:ro" \
  ghcr.io/gmouzourou/domain-in-a-box:0.6.1
```

`-e DIB_DOMAIN_ADMIN_PASSWORD` passes the value from your shell without
writing it on the command line. The NetBIOS name defaults to the realm's first
label (`AD`); add `-e DIB_DOMAIN=...` only to choose another. The certificate mount is optional; omit it to
use Samba's self-signed certificate.

Do not add `--security-opt no-new-privileges` to this controller profile:
Kea and the Stork agent acquire required executable file capabilities after
switching Unix users. The broad `SYS_ADMIN` capability is required for Samba
SYSVOL ACL operations; this is not a restricted-container security boundary.
Chrony runs without adjusting the host clock, so `SYS_TIME` is not granted.

Set `parent` to the host's LAN interface. Keep `DIB_INTERFACE="eth0"`: inside
a container attached to one macvlan network, the interface is `eth0`
whatever the host interface is called.

### Managing the container

```bash
docker logs -f domain-in-a-box
docker exec -it domain-in-a-box bash
docker restart domain-in-a-box
docker stop domain-in-a-box && docker rm domain-in-a-box   # keeps volumes
```

## Notes for both methods

- The container hostname becomes the domain controller's name. Keep it
  stable and no longer than 15 characters.
- Changes to `DIB_DHCP_POOL` and `DIB_DNS_FORWARDERS` are applied on restart.
  Other domain settings apply only at first provisioning. See
  [Operations](operations.md#restarts-and-configuration-changes).
- Compose and the CLI example pin the release's image version rather than
  `latest`. Keep that pin and upgrade deliberately. See
  [Operations](operations.md#upgrades).
