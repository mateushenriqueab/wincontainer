# 🪟 wincontainer

> Run OCI containers on Windows — no Docker, no daemon, no VM overhead.

**wincontainer** is a lightweight container runtime for Windows that uses WSL2 as the execution layer. Pull any image from Docker Hub, run it as a native WSL2 distro, and manage everything through a simple CLI.

---

## How it works

Traditional container runtimes on Windows carry a heavy stack:

```
your app → Docker daemon → containerd → Linux VM → kernel
```

wincontainer cuts straight to the point:

```
your app → WSL2 (already running) → kernel
```

---

## Features

- **No Docker Desktop required** — works with plain WSL2, which ships with Windows 10/11
- **Zero network configuration** — all containers share the host network by default, just like native Linux processes
- **Persistent storage** — data lives inside the WSL2 distro filesystem, which is persistent by nature
- **Optional volumes** — mount paths from the host when needed
- **Simple CLI** — `pull`, `start`, `stop`, `list`
- **Low overhead** — WSL2 uses dynamic memory reclaim; no fixed RAM reservation

---

## Requirements

- Windows 10 (21H2+) or Windows 11
- WSL2 enabled (`wsl --install`)
- Internet access to pull images

---

## Installation

Download the latest `wincontainer.exe` from [Releases](../../releases) and add it to your PATH.

Or clone and build from source:

```powershell
git clone https://github.com/your-username/wincontainer
cd wincontainer
go build -o wincontainer.exe .
```

---

## Usage

### Pull an image

```powershell
.\wincontainer.exe pull nginx
.\wincontainer.exe pull rabbitmq:management
```

### Start a container

```powershell
.\wincontainer.exe start nginx
.\wincontainer.exe start rabbitmq-management
```

### List running containers

```powershell
.\wincontainer.exe list
```

```
NAME                    DISTRO                              IMAGE                        STATE     VOLUMES
----                    ------                              -----                        -----     -------
mysqldb                 winc_mysqldb                        alpine/mysql                 Stopped   -
rabbitmq-management     winc_rabbitmq-management            rabbitmq:management          Running   /var/lib/rabbitmq
```

---

## Networking

Because all containers run as WSL2 distros on the same host, they share the Windows host network automatically. No bridge networks, no manual DNS, no `--network` flags.

Your Node.js app and your Postgres container talk to each other via `localhost` — exactly like processes on a native Linux machine.

---

## Tested images

Over 10 enterprise-grade images validated in a single day across completely different categories:

| Image | Category | Status |
|---|---|---|
| nginx | Web server | ✅ |
| postgres | Relational database | ✅ |
| alpine/mysql | Relational database | ✅ |
| openjdk:19-ea-jdk-alpine3.16 | JVM runtime | ✅ |
| rabbitmq:management | Message broker | ✅ |
| keycloak | Auth / IAM | ✅ |
| grafana | Observability | ✅ |
| elasticsearch | Search engine | ✅ |
| camunda/camunda-bpm-platform | BPM / Workflow engine | ✅ |
| hashicorp/vault | Secrets management | ✅ |
| minio | Object storage (S3-compatible) | ✅ |
| apache/kafka | Event streaming | ✅ |

---

## Architecture

```
Docker Hub / OCI Registry
        │
        ▼  (Go: manifest resolution + layer pull + untar + merge)
   rootfs.tar
        │
        ▼  (wsl --import winc_<name>)
   WSL2 distro  (winc_<name>)
        │
        ▼  (wsl -d winc_<name> -- <entrypoint>)
   running process
```

The `winc_` prefix is used as a namespace convention, allowing `wincontainer list` to distinguish managed containers from regular WSL2 distros without any external state file. No JSON state, no database — WSL itself is the registry.

---

## Why not just use Docker Desktop?

- Docker Desktop requires a paid license for companies with 250+ employees
- It runs a dedicated Linux VM with fixed memory allocation
- It adds daemon and containerd layers between your app and the kernel
- It requires manual network configuration for inter-container communication
- wincontainer uses WSL2, which is already present on modern Windows machines — no installation, no license, no overhead

---

## Status

This project is in active development and experimental. APIs and CLI commands may change.

---

## License

MIT
