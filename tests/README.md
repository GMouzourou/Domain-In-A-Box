# Test Suite Guide

This directory contains the Docker-based integration test environment for **Domain-In-A-Box**.

---

## Layout

| Path | Purpose |
| --- | --- |
| `docker-compose.test.yml` | Spins up the domain controller and Linux test client together |
| `linux-client/Dockerfile` | Builds the Ubuntu-based client used for AD join tests |
| `linux-client/entrypoint.sh` | Starts D-Bus, `realmd`, DHCP, Kerberos, and SSSD inside the client |
| `test-runner/` | Go CLI that executes the test suites |
| `kubernetes/` | Ephemeral kind cluster with Multus/macvlan for running the same suites against the Helm chart |

---

## Test Runner Suites

The Go runner under `test-runner/` exposes the following suites:

| Suite | Purpose |
| --- | --- |
| `health` | Confirms DNS, LDAP, Kerberos, SMB, DHCP, Stork REST, BIND metrics, and Samba Prometheus endpoint ports are reachable |
| `dns` | Validates A, SRV, and PTR lookups |
| `dhcp` | Checks DHCP reachability and DNS accessibility |
| `ad` | Verifies LDAP and AD object queries |
| `ad-linux` | Performs Linux client domain join and SSSD user lookup |

---

## Commands

From the repository root:

```bash
make test-all
make test-clean
make test-health
make test-dns
make test-dhcp
make test-ad
make test-ad-linux
```

To run the Go CLI directly:

```bash
cd tests/test-runner
go run . run all
go run . run dns
go run . run ad-linux --verbose
```

---

## Kubernetes Test Environment

`tests/kubernetes/` runs the same suites against the Helm chart on a throwaway
[kind](https://kind.sigs.k8s.io/) cluster. A dedicated Docker bridge
(`192.168.3.0/24`) is attached to the kind node and used as the macvlan master,
so the domain controller and the test client share a real L2 segment. That is
what allows the client to request an actual DHCP lease from Kea, which the
compose environment cannot do.

```bash
make k8s-all      # up + images + deploy + test
make k8s-logs     # diagnostics into artifacts/k8s
make k8s-down     # delete the cluster and the lab bridge
```

| File | Purpose |
| --- | --- |
| `kind-cluster.yaml` | Single-node cluster; both pods must share a macvlan master |
| `network-attachments.yaml` | CI-only attachments: static for the DC and Stork, IPAM-less for the client |
| `values.ci.yaml` | Chart values using side-loaded images and the CI attachments |
| `test-runner-job.yaml` | Runs the suites in-cluster with `ENABLE_DHCP_CLIENT=true` |
| `lab.sh` | Provisions the cluster, Multus, images, chart and job |

The controller points its own resolver at BIND, so the chart sets
`DIB_CLUSTER_DOMAIN` and BIND forwards that domain back to the cluster DNS the
pod started with. That is what keeps `dib-postgresql` and `dib-stork-server`
resolvable during bootstrap.

---

## Debugging Tips

### Bring the stack up without immediately tearing it down

```bash
docker-compose -f tests/docker-compose.test.yml up -d --build
```

### Check logs

```bash
docker logs domain-in-a-box-test-server
docker logs domain-in-a-box-test-client
```

### Open a shell in the client container

```bash
docker exec -it domain-in-a-box-test-client bash
```

### Useful manual checks inside the client

```bash
realm discover DOMAIN.HOME.ARPA
realm list
getent passwd administrator@domain.home.arpa
kinit Administrator@DOMAIN.HOME.ARPA
```

---

## Current Review Notes

Latest verified behavior:

- `make test-all` currently completes successfully with `Passed: 5/5`
- `make k8s-all` completes successfully with `Passed: 5/5`, including a real DHCP lease from the Kea pool
- the Linux client image is Ubuntu-based and validates `realm`, `adcli`, and `sssd` domain-join behavior
- the test client preserves Docker-managed networking by default (`ENABLE_DHCP_CLIENT=false`) and only attempts a DHCP lease when explicitly enabled for experiments

Because the Go test runner returns a non-zero exit code on suite failures, `make test-all` still correctly fails if any regression is introduced.

---

## Cleanup

```bash
make test-clean
```
