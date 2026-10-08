# syntax=docker/dockerfile:1
#
# homecloud for a NAS or any Linux host: the agent, the CLI, kei and FFmpeg in
# one image, for linux/amd64 and linux/arm64. Build with
#   docker buildx build --platform linux/amd64,linux/arm64 -t homecloud .
# Every build input is pinned: base images by digest, Node.js and the web
# app's packages by the checksums in ui/pnpm-lock.yaml, kei by its release
# checksums, FFmpeg by its signed release tarball's checksum, smartmontools by
# its Debian package version. Base images are named in full in each FROM, so
# Dependabot can keep their digests current (.github/dependabot.yml).
# Deployment: deploy/compose.yaml and the README.

# The web app, embedded in the agent. Built once, on the build machine, with
# pnpm's own image at the version ui/package.json's packageManager names (bump
# both together, with pnpm self-update; Dependabot leaves this image alone),
# which installs the Node.js that ui/package.json's devEngines.runtime pins.
FROM --platform=$BUILDPLATFORM ghcr.io/pnpm/pnpm:12.9.1@sha256:77123fd40a39db0eb8be6bddfcf0cea15e000122b9221ccce2e4415be29050f2 AS ui
WORKDIR /src/ui
COPY ui/package.json ui/pnpm-lock.yaml ui/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY ui/ ./
RUN pnpm build

# homecloud and homecloud-agent: pure Go, cross-compiled for the target.
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie@sha256:8f58fd67ea075142d947a60e0caa4317746a55118d312f027793d382c7741734 AS go
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /src/ui/dist ui/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-X main.version=${VERSION}" -o /out/ ./cmd/homecloud ./cmd/homecloud-agent

# kei, the iCloud Photos downloader: its release binary, checked against the
# release's published SHA-256 (kei v0.24.1 SHA256SUMS.txt).
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS kei
ARG TARGETARCH
ARG KEI_VERSION=0.24.1
ARG KEI_SHA256_AMD64=d88a51e29c3dcf4ee32ee4daf1435f0e2ea40606197a64c5bee306103e710c2d
ARG KEI_SHA256_ARM64=622359b6196625c041cd578fa9440906e8414d2068da09e8f6921d51cf21a051
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) asset=kei-linux-x86_64.tar.gz;  sum=$KEI_SHA256_AMD64 ;; \
      arm64) asset=kei-linux-aarch64.tar.gz; sum=$KEI_SHA256_ARM64 ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/kei.tar.gz "https://github.com/rhoopr/kei/releases/download/v${KEI_VERSION}/${asset}"; \
    echo "${sum}  /tmp/kei.tar.gz" | sha256sum -c -; \
    tar -xzf /tmp/kei.tar.gz -C /usr/local/bin kei

# FFmpeg from its release tarball (signed with FFmpeg's release key
# FCF986EA15E6E293A5644F10B4322F04D67658D8; the checksum below is of the
# verified tarball), compiled for the target with libwebp (thumbnails),
# libjxl (JPEG XL previews in DNG 1.7 files) and zlib (PNG); autodetection is
# off so nothing else slips in. Only ffmpeg and ffprobe are built, without
# network support.
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f AS ffmpeg
ARG BUILDARCH
ARG TARGETARCH
ARG FFMPEG_VERSION=9.0.2
ARG FFMPEG_SHA256=8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e
# The native C compiler (FFmpeg's build tools run on the build machine) and,
# for another architecture, Debian's cross compiler for the target.
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) cross=gcc-x86-64-linux-gnu ;; \
      arm64) cross=gcc-aarch64-linux-gnu ;; \
      *) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    if [ "$TARGETARCH" = "$BUILDARCH" ]; then cross=""; else dpkg --add-architecture "$TARGETARCH"; fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl xz-utils make pkg-config nasm gcc libc6-dev \
      $cross "libwebp-dev:$TARGETARCH" "libjxl-dev:$TARGETARCH" "zlib1g-dev:$TARGETARCH"; \
    rm -rf /var/lib/apt/lists/*
RUN set -eu; \
    curl -fsSL -o /tmp/ffmpeg.tar.xz "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz"; \
    echo "${FFMPEG_SHA256}  /tmp/ffmpeg.tar.xz" | sha256sum -c -; \
    mkdir /src; tar -xJf /tmp/ffmpeg.tar.xz -C /src --strip-components=1
WORKDIR /src
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) arch=x86_64;  triple=x86_64-linux-gnu ;; \
      arm64) arch=aarch64; triple=aarch64-linux-gnu ;; \
    esac; \
    cross=""; \
    if [ "$TARGETARCH" != "$BUILDARCH" ]; then \
      cross="--enable-cross-compile --arch=$arch --target-os=linux --cross-prefix=${triple}-"; \
    fi; \
    PKG_CONFIG_LIBDIR="/usr/lib/${triple}/pkgconfig:/usr/share/pkgconfig" ./configure \
      --prefix=/opt/ffmpeg $cross \
      --pkg-config=pkg-config \
      --enable-libwebp --enable-libjxl --enable-zlib \
      --disable-ffplay --disable-doc --disable-network --disable-debug --disable-autodetect; \
    make -j"$(nproc)"; \
    make install

# The runtime: Debian 13 (kei needs glibc 2.39 or newer).
FROM debian:trixie-slim@sha256:a29215f6a35e51e22adffa17f89e9d2ef06214e64a2bad10d765c46aea49f11f
# smartctl reads the archive drives' health (drive_health.source = "smartctl")
# in the smartctl sidecar, a container of its own (`homecloud smartctl serve`,
# deploy/compose.yaml); the agent's container has neither the drives nor any
# capability. smartmontools 7.5 comes from Debian's official backports
# (trixie has 7.4). Docker gives a non-root container user no capabilities of
# its own, so smartctl carries the ones it needs as file capabilities:
# CAP_SYS_RAWIO (ATA) and CAP_SYS_ADMIN (NVMe) to send SMART commands,
# CAP_DAC_OVERRIDE to open the drives' nodes. Nothing else in the image is
# setuid or setgid: those bits are stripped before smartctl gets its
# capabilities.
ARG SMARTMONTOOLS_VERSION=7.5-2~bpo13+1
RUN printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie-backports\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.gpg\n' \
        > /etc/apt/sources.list.d/backports.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata libdbus-1-3 libwebp7 libwebpmux3 libsharpyuv0 libjxl0.11 zlib1g libcap2-bin \
    && apt-get install -y --no-install-recommends -t trixie-backports "smartmontools=${SMARTMONTOOLS_VERSION}" \
    && find / -xdev -type f -perm /6000 -exec chmod a-s {} + \
    && setcap cap_dac_override,cap_sys_rawio,cap_sys_admin+ep /usr/sbin/smartctl \
    && apt-get purge -y libcap2-bin \
    && apt-get autoremove -y \
    && rm -rf /var/lib/apt/lists/*
COPY --from=go /out/ /opt/homecloud/bin/
COPY --from=kei /usr/local/bin/kei /opt/kei/kei
COPY --from=ffmpeg /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /opt/ffmpeg/bin/
# Mount points: the archive (storage.root), the configuration, the agent's
# and kei's state (iCloud sessions; keep it private), and ledger snapshots
# (backup.dir, on another drive than the archive).
ENV PATH=/opt/homecloud/bin:$PATH \
    HOMECLOUD_CONFIG=/config/config.toml \
    XDG_STATE_HOME=/state \
    KEI_DATA_DIR=/state/kei \
    HOME=/state/home \
    XDG_RUNTIME_DIR=/tmp/homecloud-run
# Never root by default: compose runs it as the NAS user that owns the
# folders (user: PUID:PGID), and `docker run` takes --user UID:GID.
USER 65532:65532
ENTRYPOINT ["homecloud-agent"]
