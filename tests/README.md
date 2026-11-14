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
| `health` | Queries DNS and checks TCP service connections; checks a DHCP lease when the environment requires one |
| `dns` | Validates A, SRV, and PTR lookups |
| `dhcp` | Queries DNS and checks a DHCP lease when the environment requires one |
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

CI scans pull requests with Trivy and blocks on CRITICAL or HIGH findings.
SARIF results are uploaded when repository permissions allow it.

The test runner verifies LDAPS against system CAs by default; set
`DIB_LDAP_CA_CERTIFICATE` when using a private CA. The isolated Compose test
profile explicitly opts into LDAP TLS bypass for its test client.

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
| `kind-cluster.yaml` | Single-node cluster; the DC and L2 test client use the macvlan master |
| `network-attachments.yaml` | CI-only attachments: static for the DC and IPAM-less for the client |
| `values.ci.yaml` | Chart values using side-loaded images and verified Stork/LDAP TLS |
| `test-runner-job.yaml` | Runs the suites in-cluster with `ENABLE_DHCP_CLIENT=true` |
| `lab.sh` | Provisions the cluster, Multus, images, chart and job |

The Stork server does not receive a macvlan attachment. The domain controller
advertises its Stork agent through a stable headless Service FQDN on port `8081`;
the integration job checks that the registration marker contains that
in-cluster Service FQDN. It then restarts Stork and the controller, checking
that retained state keeps the same callback endpoint. Agent
registration uses Stork's server-token flow, which also exercises the
server-to-agent TLS ping. This kind setup is single-node and does not replace
different-node validation in a multi-node RKE2 lab.

The lab generates a short-lived test CA and service certificates with the
controller and Stork Service DNS names, creates the chart's TLS Secrets, and
uses the default certificate-verification settings. Successful Stork
registration therefore exercises HTTPS chain/hostname verification. The
authenticated test-runner LDAP queries verify the controller's LDAPS
certificate. `openssl` is required by `lab.sh` to generate these temporary
certificates.

The controller points its own resolver at BIND, so the chart sets
`DIB_CLUSTER_DOMAIN` and BIND forwards that domain back to the cluster DNS the
pod started with. That is what keeps the database, Stork server, and agent
Service names resolvable during bootstrap.

The chart's rendered TLS/Secret configuration checks can be run without a
cluster:

```bash
bash tests/kubernetes/helm-chart-test.sh
```

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

Existing project baseline before the current chart changes:

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
