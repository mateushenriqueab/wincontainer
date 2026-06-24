# wincontainer

Run Linux OCI images on Windows using WSL2 — no Docker Desktop, no Docker daemon, and no traditional container runtime in the execution path.

`wincontainer` is an experimental OCI-to-WSL runtime and builder for Windows. It can pull Linux OCI images, convert their root filesystem into WSL2 distros, run image entrypoints directly through WSL, build simple Dockerfiles using WSL2 as the build execution layer, and push the result back as standard OCI images.

## Why?

Traditional container workflows on Windows often involve a heavier local stack:

```text
your app → Docker daemon → containerd → Linux VM → kernel
```

`wincontainer` explores a smaller execution path:

```text
OCI image → WSL2 distro → process
```

For builds, it uses WSL2 as the build environment:

```text
Dockerfile → WSL2 builder distro → final rootfs → OCI image
```

The goal is not to replace Docker, Docker Desktop, Kubernetes, BuildKit, or containerd. The goal is to explore WSL2 as a lightweight execution and build layer for local Linux workloads on Windows.

## Features

* **No Docker Desktop required** — uses WSL2 directly.
* **No Docker daemon** — images are pulled, unpacked, built, and pushed by the CLI.
* **OCI image support** — pulls images from OCI-compatible registries.
* **WSL2-native execution** — each workload runs as a dedicated WSL2 distro.
* **Dockerfile build support** — builds simple single-stage Dockerfiles using WSL2 as the build execution layer.
* **OCI push support** — publishes local workloads as standard OCI images.
* **Docker interoperability** — images built and pushed by `wincontainer` can be pulled and executed by Docker.
* **Simple networking** — workloads use WSL localhost forwarding; if an app listens on `5432`, use `localhost:5432`.
* **Persistent filesystem** — data persists inside the WSL2 distro until the workload is deleted.
* **OCI-declared volumes** — declared volume paths are detected and prepared inside the distro.
* **Low local overhead** — no extra Docker daemon/containerd layer during execution.

## Requirements

* Windows 10 21H2+ or Windows 11
* WSL2 enabled
* Internet access to pull and push OCI images
* Go, if building from source

## Installation

Download the latest `wincontainer.exe` from Releases and add it to your PATH.

Or clone and build from source:

```powershell
git clone https://github.com/mateushenriqueab/wincontainer
cd wincontainer
go build -o wincontainer.exe .
```

## CLI

```text
wincontainer build -t <name> [-f Dockerfile] <context>
wincontainer push <name> <target-ref>
wincontainer pull <image> [name]
wincontainer start <name> [-e KEY=value] [-d] [-- command args]
wincontainer list
wincontainer delete <name>
```

## Usage

### Pull an image

```powershell
wincontainer pull nginx nginx
wincontainer pull rabbitmq:management rabbitmq-management
```

### Start a workload

```powershell
wincontainer start nginx

wincontainer start rabbitmq-management -d `
  -e RABBITMQ_DEFAULT_USER=user `
  -e RABBITMQ_DEFAULT_PASS=password
```

### List workloads

```powershell
wincontainer list
```

Example:

```text
NAME                    DISTRO                              IMAGE                        STATE       VOLUMES
----                    ------                              -----                        -----       -------
nginx                   winc_nginx                          nginx                        Running     -
rabbitmq-management     winc_rabbitmq-management            rabbitmq:management          Running     /var/lib/rabbitmq
```

## Build

`wincontainer build` supports simple single-stage Dockerfiles.

Example:

```dockerfile
FROM alpine:latest
RUN apk add --no-cache curl
CMD ["curl", "--version"]
```

Build and run:

```powershell
wincontainer build -t curl-test .
wincontainer start curl-test
```

Another example using Java:

```dockerfile
FROM eclipse-temurin:21-jdk-alpine

WORKDIR /app

RUN echo 'public class Main { public static void main(String[] args) { System.out.println("hello from java build"); } }' > Main.java
RUN /opt/java/openjdk/bin/javac Main.java

CMD ["/opt/java/openjdk/bin/java", "Main"]
```

```powershell
wincontainer build -t javinha .
wincontainer start javinha
```

Expected output:

```text
hello from java build
```

### Supported Dockerfile instructions

Current build support includes:

```text
FROM
RUN
WORKDIR
COPY
ADD
ENV
EXPOSE
VOLUME
USER
CMD
ENTRYPOINT
```

Current build limitations:

* single-stage only
* no layer cache
* no multi-stage `COPY --from`
* no advanced BuildKit features
* no `.dockerignore` support yet
* final pushed image is currently exported as a single OCI layer

## Push

`wincontainer push` publishes a local workload as a standard OCI image.

Example using GitHub Container Registry:

```powershell
wincontainer push curl-test ghcr.io/<user>/curl-test:latest
```

Example:

```powershell
wincontainer push javinha ghcr.io/mateushenriqueab/javinha:latest
```

Then the image can be pulled and executed by Docker:

```powershell
docker run --rm ghcr.io/mateushenriqueab/javinha:latest
```

Expected output:

```text
hello from java build
```

This validates the full interoperability path:

```text
wincontainer build
        │
        ▼
wincontainer push
        │
        ▼
OCI registry
        │
        ▼
docker run
```

## Full cycle without Docker daemon

Example:

```powershell
wincontainer build -t javinha .
wincontainer push javinha ghcr.io/mateushenriqueab/javinha:latest
wincontainer pull ghcr.io/mateushenriqueab/javinha:latest
wincontainer start ghcr-io-mateushenriqueab-javinha-latest
```

Expected output:

```text
hello from java build
```

The same image can also be executed by Docker:

```powershell
docker run --rm ghcr.io/mateushenriqueab/javinha:latest
```

Expected output:

```text
hello from java build
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

To change ports, configure the application itself.

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

Delete a workload:

```powershell
wincontainer delete rabbitmq-management
```

This unregisters the WSL2 distro and removes the local WinContainer workload data.

## Architecture

### Pull and run

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

### Build

```text
Dockerfile
        │
        ▼
base OCI image
        │
        ▼
temporary WSL2 builder distro
        │
        ▼
RUN / COPY / ENV / CMD / ENTRYPOINT
        │
        ▼
final rootfs.tar
        │
        ▼
local wincontainer workload
```

### Push

```text
local rootfs.tar + metadata.json
        │
        ▼
OCI config + single filesystem layer
        │
        ▼
OCI registry
        │
        ▼
docker pull / docker run compatible image
```

The `winc_` prefix is used as a namespace convention so `wincontainer list` can distinguish managed workloads from regular WSL2 distros.

## Tested images

Validated local workloads:

| Image                 | Category                     | Status |
| --------------------- | ---------------------------- | :----: |
| `nginx`               | Web server                   |    ✅   |
| `redis`               | Cache                        |    ✅   |
| `postgres`            | Relational database          |    ✅   |
| `alpine/mysql`        | Relational database          |    ✅   |
| `openjdk`             | JVM runtime                  |    ✅   |
| `rabbitmq:management` | Message broker               |    ✅   |
| `keycloak`            | Auth / IAM                   |    ✅   |
| `grafana`             | Observability                |    ✅   |
| `elasticsearch`       | Search engine                |    ✅   |
| `minio`               | S3-compatible object storage |    ✅   |
| `apache/kafka`        | Event streaming              |    ✅   |

Validated build and push flows:

| Scenario                               | Status |
| -------------------------------------- | :----: |
| Build from Dockerfile                  |    ✅   |
| `RUN apk add --no-cache curl`          |    ✅   |
| Java compile with `javac` during build |    ✅   |
| Push to GitHub Container Registry      |    ✅   |
| Pull pushed image with `wincontainer`  |    ✅   |
| Run pushed image with `wincontainer`   |    ✅   |
| Run pushed image with Docker           |    ✅   |

Some images may require additional compatibility work, especially images that rely on systemd, privileged kernel settings, Docker-specific networking, custom users, unusual entrypoints, hardlinks, or shell-less execution.

## Known limitations

`wincontainer` is experimental. Current limitations include:

* no Docker-style port mapping
* no daemon
* no container supervision
* no cgroups/container isolation model equivalent to Docker
* no multi-stage build support yet
* no build cache yet
* no Docker Compose support
* no image signing
* pushed images are currently flattened into a single OCI layer
* some images may assume Docker/containerd-specific behavior

## Project status

This project is experimental and under active development.

It is not affiliated with Microsoft, Docker, Docker Desktop, containerd, or the official Windows Containers platform.

## License

MIT
