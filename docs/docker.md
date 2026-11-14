# Docker

Docker runs a single domain controller on one host. Choose:

- **Docker Compose** for the full stack: the controller, the Stork monitoring
  server and a bundled PostgreSQL database.
- **Docker CLI** for the core services only (AD, DNS, DHCP and NTP), without
  monitoring.

Both use a macvlan network so the controller has its own LAN address. Read
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

- Set `DIB_REALM` (for example `AD.HOME.ARPA`) and `DIB_DOMAIN` (the NetBIOS
  name, for example `AD`).
- Set unique values for `DIB_DOMAIN_ADMIN_PASSWORD`, `DIB_LDAP_PASSWORD` and
  `STORK_DATABASE_PASSWORD`.
- Set `DIB_PARENT_INTERFACE` to the host interface on your LAN (for example
  `eth0` or `enp3s0`). Leave `DIB_INTERFACE` as `eth0`, the interface name
  inside the container.
- Set `DIB_DHCP_POOL` to a free range and `DIB_DNS_FORWARDERS` to your upstream
  resolvers.

Edit the addresses in `docker-compose.yml` to match your LAN:

| Setting | Default | Meaning |
| --- | --- | --- |
| `networks.domain_net.ipam.config.subnet` | `192.168.1.0/24` | LAN subnet |
| `networks.domain_net.ipam.config.gateway` | `192.168.1.254` | LAN gateway |
| `domain-controller` `ipv4_address` | `192.168.1.1` | Controller address |
| `postgres` `ipv4_address` | `192.168.1.2` | Bundled database address |
| `stork-server` `ipv4_address` | `192.168.1.3` | Stork web UI address |

If you change the controller or Stork address, also set `STORK_LDAP_URL`
(default `ldaps://192.168.1.1:636`), `STORK_SERVER_HOST` and
`STORK_DATABASE_HOST` in `.env`, and issue the certificates for the new
addresses.

The bundled PostgreSQL service currently has a LAN address and accepts
unencrypted connections, protected by its password. Keep that password strong.
Tracking: [#46](https://github.com/GMouzourou/Domain-In-A-Box/issues/46).

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
| `postgres-data` | PostgreSQL data | Stork database |

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
  --privileged \
  --network domain_net \
  --ip 192.168.1.1 \
  --hostname domain-server \
  -e DIB_REALM="AD.HOME.ARPA" \
  -e DIB_DOMAIN="AD" \
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
  ghcr.io/gmouzourou/domain-in-a-box:0.5.1
```

`-e DIB_DOMAIN_ADMIN_PASSWORD` passes the value from your shell without
writing it on the command line. The certificate mount is optional; omit it to
use Samba's self-signed certificate.

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
