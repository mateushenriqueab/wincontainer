# wincontainer

Run Linux OCI images on Windows using WSL2 — no Docker Desktop, no Docker daemon, and no traditional container runtime in the execution path.

`wincontainer` is an experimental OCI-to-WSL runner for Windows. It pulls Linux OCI images, converts their root filesystem into a lightweight WSL2 distro, and starts the image entrypoint directly through WSL.

## Why?

Traditional container workflows on Windows often involve a heavier stack:

```text
your app → Docker daemon → containerd → Linux VM → kernel
```

`wincontainer` keeps the execution path smaller:

```text
OCI image → WSL2 distro → process
```

The goal is not to replace Docker or Kubernetes. The goal is to provide a lightweight way to run local Linux workloads on Windows using WSL2 as the execution layer.

## Features

* **No Docker Desktop required** — uses WSL2 directly.
* **No Docker daemon** — images are pulled and unpacked by the CLI.
* **OCI image support** — pulls images from OCI-compatible registries.
* **WSL2-native execution** — each workload runs as a dedicated WSL2 distro.
* **Simple networking** — workloads use WSL localhost forwarding; if an app listens on `5432`, use `localhost:5432`.
* **Persistent filesystem** — data persists inside the WSL2 distro until the workload is deleted.
* **OCI-declared volumes** — declared volume paths are detected and prepared inside the distro.
* **Simple CLI** — pull, start, stop, list, logs, stats, delete.
* **Low local overhead** — no extra Docker daemon/containerd layer during execution.

## Requirements

* Windows 10 21H2+ or Windows 11
* WSL2 enabled
* Internet access to pull OCI images

## Installation

Download the latest `wincontainer.exe` from Releases and add it to your PATH.

Or clone and build from source:

```powershell
git clone https://github.com/mateushenriqueab/wincontainer
cd wincontainer
go build -o wincontainer.exe .
```

## Usage

### Pull an image

```powershell
.\wincontainer.exe pull nginx nginx
.\wincontainer.exe pull rabbitmq:management rabbitmq-management
```

### Start a workload

```powershell
.\wincontainer.exe start nginx
.\wincontainer.exe start rabbitmq-management -d -e RABBITMQ_DEFAULT_USER=user -e RABBITMQ_DEFAULT_PASS=password
```

### List workloads

```powershell
.\wincontainer.exe list
```

Example:

```text
NAME                    DISTRO                              IMAGE                        STATE       VOLUMES
----                    ------                              -----                        -----       -------
nginx                   winc_nginx                          nginx                        Running     -
rabbitmq-management     winc_rabbitmq-management            rabbitmq:management          Running     /var/lib/rabbitmq
```

## Networking

`wincontainer` does not implement Docker-style port mapping.

Instead, it relies on WSL2 localhost forwarding. If a workload listens on a port inside WSL, that same port is available from Windows through `localhost`.

Examples:

```text
PostgreSQL  → localhost:5432
RabbitMQ    → localhost:5672
RabbitMQ UI → localhost:15672
MinIO       → localhost:9000
MinIO UI    → localhost:9001
```

## Storage

Each workload is imported as its own WSL2 distro using the `winc_` prefix.

Declared OCI volumes are detected automatically. For example, `rabbitmq:management` declares:

```text
/var/lib/rabbitmq
```

`wincontainer` creates that path inside the WSL2 distro and keeps the data there until the workload is deleted.

```text
winc_rabbitmq-management
└── /var/lib/rabbitmq
```

## Tested images

Validated local workloads:

| Image                 |                     Category | Status |
| --------------------- | ---------------------------: | :----: |
| `nginx`               |                   Web server |    ✅   |
| `redis`               |                        Cache |    ✅   |
| `postgres`            |          Relational database |    ✅   |
| `alpine/mysql`        |          Relational database |    ✅   |
| `openjdk`             |                  JVM runtime |    ✅   |
| `rabbitmq:management` |               Message broker |    ✅   |
| `keycloak`            |                   Auth / IAM |    ✅   |
| `grafana`             |                Observability |    ✅   |
| `elasticsearch`       |                Search engine |    ✅   |
| `minio`               | S3-compatible object storage |    ✅   |
| `apache/kafka`        |              Event streaming |    ✅   |

Some images may require additional compatibility work, especially images that rely on systemd, privileged kernel settings, Docker-specific networking, custom users, or shell-less entrypoints.

## Architecture

```text
Docker Hub / OCI Registry
        │
        ▼
manifest + layers
        │
        ▼
rootfs.tar
        │
        ▼
wsl --import winc_<name>
        │
        ▼
WSL2 distro
        │
        ▼
entrypoint/cmd as process
```

The `winc_` prefix is used as a namespace convention so `wincontainer list` can distinguish managed workloads from regular WSL2 distros.

## Project status

This project is experimental and under active development.

It is not affiliated with Microsoft, Docker, or the official Windows Containers platform.

## License

MIT
