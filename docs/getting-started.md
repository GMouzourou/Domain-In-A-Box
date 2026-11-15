# Getting started

## What you are deploying

Domain-In-A-Box runs a complete set of network directory services as one
controller with its own address on your LAN:

| Component | Software | Provides |
| --- | --- | --- |
| Domain controller | Samba AD DC | Active Directory, Kerberos, LDAP/LDAPS, SMB (SYSVOL and NETLOGON), Group Policy storage |
| DNS | Samba AD DNS with BIND9 | Samba answers clients on port 53 for the AD zones; BIND resolves everything else through your upstream forwarders |
| DHCP | Kea DHCPv4 and dynamic DNS | Address leases, with client names registered in AD DNS using GSS-TSIG |
| Time | Chrony | NTP for domain members, so Kerberos clocks stay aligned |
| Monitoring | ISC Stork agent and server, PostgreSQL | Dashboards for Kea and BIND, Prometheus metrics for Samba, BIND and Kea |

The controller container is the core. The Stork server and its PostgreSQL
database are separate containers or pods. The domain keeps running while
monitoring is unavailable.

## Prerequisites

All deployments need:

- A Linux host (or Kubernetes nodes) on the same layer-2 network as the
  clients, if the controller is to serve DHCP.
- A free static IPv4 address for the controller on that network.
- A DHCP pool that does not overlap any other DHCP server's range. Disable
  your router's DHCP server or move its range before you enable DHCP here.
- A domain name you control. A subdomain of `home.arpa` (for example
  `ad.home.arpa`) is a safe choice for private networks. Avoid `.local`, which
  conflicts with multicast DNS.
- Strong, unique passwords for the domain administrator and, when monitoring
  is enabled, the Stork LDAP account and database.
- TLS certificates when monitoring is enabled. See [Certificates](certificates.md).

The controller runs privileged because Samba, BIND and Kea need low-level
network access. Run it only on hosts you trust and control.

## Choosing a deployment

| | Docker Compose or CLI | Kubernetes, single controller | Kubernetes, multi-controller (experimental) |
| --- | --- | --- | --- |
| Best for | Home networks, labs, a single small site | Clusters you already run; declarative, Helm-managed installs | Evaluating redundancy in a lab |
| Infrastructure | One Linux host with Docker | A cluster with Multus and persistent storage | At least two physical nodes with Multus, node-pinned storage, and two LAN addresses |
| Monitoring | Compose includes Stork and PostgreSQL; the CLI path is core-only | Stork included; bundled or external PostgreSQL | Shared Stork server on the primary |
| Redundancy | None | None (the pod restarts on the same data) | Two domain controllers; AD, DNS and Kerberos continue if one is lost |
| Operator involvement | Restart or rebuild the host if it fails | Restore the node or its storage if it fails | Some outages need a deliberate manual step (see below) |
| Support status | Supported | Supported | Experimental: not production-ready |

Multi-controller mode keeps authentication and name resolution available
when one host fails. DHCP takeover and permanent replacement of a failed
controller remain manual, so it adds operational responsibility. Read
[Multi-controller Kubernetes](multi-controller.md) before choosing it.

## Next steps

1. Plan addressing and firewall rules: [Networking](networking.md).
2. Create certificates: [Certificates](certificates.md).
3. Deploy: [Docker](docker.md), [Kubernetes](kubernetes.md) or
   [Multi-controller Kubernetes](multi-controller.md).
4. Join a client. On Windows, set the controller as the DNS server and join
   the domain from **System > About > Domain or workgroup**. On Ubuntu, use
   `realm join <domain>` (realmd, SSSD and adcli).
5. Manage users and Group Policy with Windows RSAT (Active Directory Users and
   Computers, Group Policy Management) or `samba-tool` inside the controller.
