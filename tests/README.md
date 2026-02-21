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

---

## Test Runner Suites

The Go runner under `test-runner/` exposes the following suites:

| Suite | Purpose |
| --- | --- |
| `health` | Confirms DNS, LDAP, Kerberos, SMB, and DHCP ports are reachable |
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

## Debugging Tips

### Bring the stack up without immediately tearing it down

```bash
docker-compose -f tests/docker-compose.test.yml up -d --build
```

### Check logs

```bash
docker logs domain-in-a-box-test
docker logs domain-in-a-box-test-runner
```

### Open a shell in the client container

```bash
docker exec -it domain-in-a-box-test-runner bash
```

### Useful manual checks inside the client

```bash
realm discover HOME.ARPA
realm list
getent passwd administrator@home.arpa
kinit Administrator@HOME.ARPA
```

---

## Current Review Notes

Latest verified behavior:

- `health`, `dhcp`, and `ad` are passing
- `ad-linux` is implemented and domain join works in the test client flow
- `dns` is mostly passing, but the reverse PTR assertion still needs follow-up

Because the test runner now returns a non-zero exit code on suite failures, `make test-all` correctly fails while unresolved DNS issues remain.

---

## Cleanup

```bash
make test-clean
```
