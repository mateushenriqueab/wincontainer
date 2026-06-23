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

Each container image is pulled from an OCI registry, its layers are merged into a `rootfs.tar`, and imported as a WSL2 distro prefixed with `winc_`. From there, WSL2 handles execution natively — no daemon, no extra VM, no license required.

---

## Features

- **No Docker Desktop required** — works with plain WSL2, which ships with Windows 10/11
- **Zero network configuration** — all containers share the host network by default, just like native Linux processes
- **Persistent storage** — data lives inside the WSL2 distro filesystem, which is persistent by nature
- **Optional volumes** — mount paths from the host when needed
- **Simple CLI** — `pull`, `start`, `stop`, `list`, `exec`
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
.\wincontainer.exe pull alpine/mysql
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
nginx                   winc_nginx                          nginx                        Stopped   -
openjdk-19-ea-...       winc_openjdk-19-ea-...              openjdk:19-ea-jdk-alpine3.16 Stopped   -
rabbitmq-management     winc_rabbitmq-management            rabbitmq:management          Running   /var/lib/rabbitmq
```

### Execute a command inside a container

```powershell
.\wincontainer.exe exec nginx bash
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

> Camunda ran with 2 process definitions deployed and 8 active running instances. RabbitMQ came up with Erlang 27.3.4.13 and a live queue. Elasticsearch 8.19.16 with Lucene 9.12.2 passed without manual kernel tuning. None of them knew they were running inside something built from scratch.

---

## Screenshots

### Container list

![wincontainer list](docs/list.png)

### nginx running on localhost

![nginx](docs/nginx.png)

### RabbitMQ Management UI — fully operational

RabbitMQ 4.3.2 with Erlang 27.3.4.13, cluster up, queues running — and it has no idea it's inside something you built from scratch.

![rabbitmq management](docs/rabbitmq.png)

### PostgreSQL via DBeaver

![postgres](docs/postgres.png)

### Keycloak — Auth & IAM

![keycloak](docs/keycloak.png)

### Grafana — Observability

![grafana](docs/grafana.png)

### Elasticsearch 8.19.16

![elasticsearch](docs/elasticsearch.png)

### Camunda BPM — Workflow engine with live process instances

![camunda](docs/camunda.png)

### HashiCorp Vault — Secrets management

![vault](docs/vault.png)

### MinIO — S3-compatible object storage

![minio](docs/minio.png)

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
