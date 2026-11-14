# Certificates

Domain-In-A-Box does not generate or publish certificates, and it ships no
default keys. Supply your own, issued by a CA your clients and services trust.
A private CA is fine for a home network or lab.

## What needs a certificate

| Certificate | Used for | Required when |
| --- | --- | --- |
| Controller | Samba LDAPS (`636`), Global Catalog over TLS (`3269`), and the Kea HA peer channel | Always recommended; required for Stork LDAP login and for DHCP HA |
| Stork server | Stork web UI and REST API over HTTPS | Stork is enabled |
| PostgreSQL CA | Verifying an external PostgreSQL server | Stork uses an external database |

Use separate key pairs for the controller and the Stork server. Both
certificates can be issued by the same CA.

Without a controller certificate, Samba uses a self-signed certificate.
Clients that verify LDAPS will reject it.

## Required names

The certificate subject alternative names (SANs) must match how each service
is reached.

| Deployment | Controller certificate SANs | Stork server certificate SANs |
| --- | --- | --- |
| Docker Compose (defaults) | IP `192.168.1.1` and DNS `domain-server.<your-dns-domain>` | IP `192.168.1.3` (or your `STORK_SERVER_HOST`) |
| Kubernetes | DNS `<fullname>-svc.<namespace>.svc.cluster.local`, or the `tls.ldap.serverName` you configure | DNS `<fullname>-stork-server.<namespace>.svc.cluster.local` |
| Kubernetes with DHCP HA | Also the controller's LAN IP address | Unchanged |

`<fullname>` is the Helm release name, followed by `-domain-in-a-box` unless
the release name already contains `domain-in-a-box` or you set
`fullnameOverride`. For a release named `domain-in-a-box` in namespace
`domain-in-a-box`, the names are `domain-in-a-box-svc.domain-in-a-box.svc.cluster.local`
and `domain-in-a-box-stork-server.domain-in-a-box.svc.cluster.local`.
Run `helm template` and check the rendered Service names if you are unsure.

## Creating a private CA and certificates

The following example uses `openssl` to create a CA and the two Docker
Compose certificates. Run it in a protected directory and keep `ca.key`
offline once you have issued the certificates.

```bash
umask 077
mkdir -p certs/controller certs/stork-server

# Certificate authority (valid for 10 years)
openssl req -x509 -newkey rsa:4096 -sha256 -days 3650 -nodes \
  -subj "/CN=Domain-In-A-Box CA" -keyout ca.key -out ca.crt

issue() {  # issue <name> <subjectAltName>
  openssl req -new -newkey rsa:2048 -nodes -subj "/CN=$1" \
    -keyout "$1.key" -out "$1.csr"
  openssl x509 -req -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial \
    -days 825 -sha256 -out "$1.crt" \
    -extfile <(printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth,clientAuth\n' "$2")
}

issue controller "IP:192.168.1.1,DNS:domain-server.domain.home.arpa"
issue stork-server "IP:192.168.1.3"

cp controller.crt certs/controller/cert.pem
cp controller.key certs/controller/key.pem
cp ca.crt certs/controller/ca.pem
cp stork-server.crt certs/stork-server/cert.pem
cp stork-server.key certs/stork-server/key.pem
cp ca.crt certs/stork-server/ca.crt
```

`clientAuth` is included because the Kea HA peers authenticate each other
with their controller certificates.

To trust the CA on clients, install `ca.crt` on Windows with
`certutil -addstore Root ca.crt` (or Group Policy), and on Ubuntu by copying
it to `/usr/local/share/ca-certificates/` and running
`sudo update-ca-certificates`.

## Docker Compose file layout

| Host directory | Files | Mounted at |
| --- | --- | --- |
| `./certs/controller` (`DIB_CONTROLLER_TLS_DIR`) | `cert.pem`, `key.pem`, `ca.pem` | Controller `/certs`; Stork server LDAP trust |
| `./certs/stork-server` (`DIB_STORK_SERVER_TLS_DIR`) | `cert.pem`, `key.pem`, `ca.crt` | Stork server `/certs`; controller trust for Stork HTTPS |
| `./certs/postgresql` (`DIB_POSTGRES_TLS_DIR`) | `ca.crt` | External PostgreSQL CA, if used |

The controller needs either all three of its files or none.

## Kubernetes Secrets

```bash
kubectl -n domain-in-a-box create secret generic domain-in-a-box-controller-tls \
  --from-file=tls.crt=controller.crt \
  --from-file=tls.key=controller.key \
  --from-file=ca.crt=ca.crt
kubectl -n domain-in-a-box create secret generic domain-in-a-box-stork-server-tls \
  --from-file=tls.crt=stork-server.crt \
  --from-file=tls.key=stork-server.key \
  --from-file=ca.crt=ca.crt
```

The Secret names and keys can be changed with the `tls.*` values. See the
[configuration reference](configuration.md#helm-values). cert-manager can
manage these Secrets if it writes the same keys.

## Insecure options

Plain HTTP for Stork, skipped LDAP certificate verification, and unverified
PostgreSQL connections are blocked unless you explicitly opt in. Docker uses
the `DIB_ALLOW_INSECURE_*` variables; Kubernetes uses the matching
`allowInsecure*` values. Use them only in isolated tests.

## Rotation

Certificates are loaded at startup. See
[Operations: certificate rotation](operations.md#certificate-rotation).
