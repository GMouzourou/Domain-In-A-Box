# Domain-In-A-Box Manual

This manual covers installing, configuring and operating Domain-In-A-Box. For
an overview of the project, start with the [project home page](../README.md).

## Before you install

- [Getting started](getting-started.md): concepts, prerequisites and choosing a
  deployment.
- [Networking](networking.md): LAN addressing, macvlan and Multus, firewall
  ports, and the host-to-container limitation.
- [Certificates](certificates.md): the TLS certificates each deployment needs
  and how to create them with a private CA.

## Deploy

- [Docker](docker.md): Docker Compose (with monitoring) and Docker CLI
  (core services only).
- [Kubernetes](kubernetes.md): a single controller installed with the Helm
  chart.
- [Multi-controller Kubernetes](multi-controller.md) (**experimental**): two
  domain controllers on separate hosts.

## Operate

- [Configuration reference](configuration.md): environment variables and Helm
  values.
- [Operations](operations.md): monitoring, restarts, upgrades, certificate
  rotation and backups.
- [Operator procedures](operator-procedures.md): DHCP takeover, permanent
  controller loss and controller replacement for multi-controller deployments.
- [Troubleshooting](troubleshooting.md): common failures and diagnostics.

## Which page to read

Each topic has one home; other pages link to it rather than repeat it.

| Need | Page |
| --- | --- |
| Decide between Docker, Kubernetes and multi-controller | [Getting started](getting-started.md) |
| Install a deployment, step by step | [Docker](docker.md), [Kubernetes](kubernetes.md) or [Multi-controller Kubernetes](multi-controller.md) |
| Look up a variable or Helm value | [Configuration reference](configuration.md) |
| Routine work on any deployment: upgrades, rotation, backups | [Operations](operations.md) |
| Act on a failed controller in a multi-controller deployment | [Operator procedures](operator-procedures.md) |
| Understand why multi-controller mode works the way it does | [Multi-controller design](multi-controller-design.md) |

## Background and contributing

- [Multi-controller design](multi-controller-design.md): design rationale and
  validation evidence for the experimental multi-controller mode.
- [Development](development.md): building, testing and contributing.
