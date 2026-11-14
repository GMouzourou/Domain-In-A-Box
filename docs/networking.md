# Networking

## Why the controller needs its own LAN address

Clients find a domain controller by its IP address and DNS SRV records, and
DHCP relies on layer-2 broadcasts. Domain-In-A-Box therefore runs with its own
address on your LAN instead of behind a NAT or a port-forwarded host address:

- **Docker** uses a [macvlan network](https://docs.docker.com/engine/network/drivers/macvlan/).
  Port publishing (`ports:`, `-p`) has no effect for a macvlan-only container.
- **Kubernetes** uses [Multus](https://github.com/k8snetworkplumbingwg/multus-cni)
  to add a second macvlan interface (normally `net1`) to the pod. The Helm
  chart creates the NetworkAttachmentDefinition. A normal Kubernetes
  ClusterIP or LoadBalancer Service is not enough for DHCP or for clients
  that need a stable DC address.

Choose addresses outside every DHCP pool, including the router's. Set
`DIB_INTERFACE` (Docker) or `domain.interface` (Kubernetes, default `net1`) to
the interface that carries the LAN address inside the container.

Some switches, Wi-Fi access points and hypervisor virtual NICs drop traffic
from additional MAC addresses. If the controller is unreachable, allow
promiscuous mode or MAC address changes on that port or virtual NIC, or use a
wired port.

## Host-to-container limitation

Linux macvlan isolates the parent host from its own macvlan children. The host
running the controller cannot reach the controller's LAN address, although
other machines can. Symptoms include DNS timeouts and failed domain joins from
the host itself.

To let the host reach the controller, add a macvlan "shim" interface on the
host and route the controller's address through it. For example, with parent
`eth0`, a spare address `192.168.1.250` and the controller at `192.168.1.1`:

```bash
sudo ip link add dib-shim link eth0 type macvlan mode bridge
sudo ip addr add 192.168.1.250/32 dev dib-shim
sudo ip link set dib-shim up
sudo ip route add 192.168.1.1/32 dev dib-shim
```

These commands do not persist across a reboot. Make them permanent with your
distribution's network configuration (for example netplan or
systemd-networkd).

In a multi-controller deployment, each node needs the shim to reach its local
controller. After a peer failure, the local controller may be the only one
available.

## Firewall ports

Allow these ports between clients and the controller's LAN address:

| Port | Protocol | Service |
| --- | --- | --- |
| 53 | TCP, UDP | DNS |
| 67, 68 | UDP | DHCP |
| 88 | TCP, UDP | Kerberos |
| 123 | UDP | NTP |
| 135 | TCP | RPC endpoint mapper |
| 137, 138 | UDP | NetBIOS name and datagram |
| 139 | TCP | NetBIOS session |
| 389 | TCP, UDP | LDAP |
| 445 | TCP | SMB |
| 464 | TCP, UDP | Kerberos password change |
| 636 | TCP | LDAPS |
| 3268, 3269 | TCP | Global Catalog, Global Catalog over TLS |
| 49152-49252 | TCP | Samba dynamic RPC (fixed range) |
| 9119 | TCP | BIND Prometheus metrics |
| 9547 | TCP | Kea Prometheus metrics |
| 9922 | TCP | Samba Prometheus metrics |

Multi-controller deployments also need these between the two controllers:

| Port | Protocol | Direction | Service |
| --- | --- | --- | --- |
| 53, 88, 135, 389, 445, 464, 636, 3268, 49152-49252 | TCP (and UDP where listed above) | Both | AD replication and DC-to-DC traffic |
| 22 (configurable) | TCP | Standby to writer | SYSVOL synchronization (SSH, read-only) |
| 8005 | TCP | Both | Kea HA peer channel (mutual TLS), only when DHCP HA is enabled |

In Kubernetes, the Stork server and the agent callback (port `8081`) use
cluster Services on the pod network and are not exposed on the LAN.

## Kubernetes routing

The Multus attachment adds a host route to the gateway without replacing the
pod's default route, so cluster DNS and Services keep working. Do not route
the controller's LAN address or its connected subnet through the gateway.

The controller points its own resolver at BIND. The chart sets the cluster DNS
domain (`clusterDNS.domain`, default `cluster.local`) so that BIND forwards
it to the cluster DNS and in-cluster Service names stay resolvable.
