# vanguard microVM devbox ("vmbox").
#
# Each box is a container (with /dev/kvm) that boots a Firecracker microVM.
# The guest is the devbox image plus systemd and the Docker engine, so
# quickstarts that use `systemctl` or `docker compose` work. vanguard talks
# to the in-guest agent over HTTP; the container only provides networking.
#
# Build context (written by `vanguard devbox build --backend firecracker`):
#   Dockerfile, entrypoint.sh, vanguard-agent.service, agent/{main.go,go.mod}

ARG DEVBOX_IMAGE=vanguard-devbox:latest
ARG FIRECRACKER_VERSION=v1.17.0
ARG KERNEL_URL=https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.15/x86_64/vmlinux-6.1.155

# ── Guest agent (static) ────────────────────────────────────────────────
FROM ${DEVBOX_IMAGE} AS agent
COPY agent/ /src/
RUN cd /src && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vanguard-agent .

# ── Guest root filesystem ───────────────────────────────────────────────
FROM ${DEVBOX_IMAGE} AS guest
RUN apt-get update && apt-get install -y --no-install-recommends \
        systemd systemd-sysv dbus udev kmod iproute2 iptables \
        docker.io docker-compose-v2 \
    && rm -rf /var/lib/apt/lists/* \
    # The guest kernel has legacy iptables, not nftables.
    && update-alternatives --set iptables /usr/sbin/iptables-legacy \
    && update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy
COPY --from=agent /out/vanguard-agent /usr/local/bin/vanguard-agent
COPY vanguard-agent.service /etc/systemd/system/vanguard-agent.service
COPY vanguard-init /sbin/vanguard-init
# The image's ENV doesn't reach systemd services or login shells in a VM,
# so carry it over via /etc/profile.d and /etc/environment.
RUN printf '%s\n' \
        'export VIRTUAL_ENV=/opt/python' \
        'export PATH=/opt/python/bin:/root/.local/bin:/root/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin' \
        'export LANG=C.UTF-8 LC_ALL=C.UTF-8' \
        > /etc/profile.d/vanguard.sh \
    && printf 'PATH=/opt/python/bin:/root/.local/bin:/root/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nLANG=C.UTF-8\n' > /etc/environment \
    && echo devbox > /etc/hostname \
    # The guest kernel has no iptables "raw" table, which Docker 28+ uses for
    # direct-access filtering; Docker documents this switch for such kernels.
    # TODO: custom kernel with CONFIG_IP_NF_RAW, then drop this.
    && mkdir -p /etc/systemd/system/docker.service.d \
    && printf '[Service]\nEnvironment=DOCKER_INSECURE_NO_IPTABLES_RAW=1\n' > /etc/systemd/system/docker.service.d/vanguard.conf \
    # Root is an overlay set up by /sbin/vanguard-init, so no fstab entries.
    && : > /etc/fstab \
    && chmod 0755 /sbin/vanguard-init && mkdir -p /overlay /newroot \
    && systemctl enable vanguard-agent.service docker.service \
    && systemctl mask serial-getty@ttyS0.service getty@tty1.service \
        systemd-networkd.service systemd-resolved.service systemd-timesyncd.service \
        apt-daily.timer apt-daily-upgrade.timer motd-news.timer \
    && mkdir -p /work/project

# ── ext4 image of the guest (built as root here, so ownership is right) ──
FROM ubuntu:24.04 AS rootfs
RUN apt-get update && apt-get install -y --no-install-recommends e2fsprogs && rm -rf /var/lib/apt/lists/*
RUN --mount=type=bind,from=guest,target=/guest \
    size=$(du -sm --exclude=/guest/proc --exclude=/guest/sys /guest | cut -f1) \
    && mkdir /out \
    && mkfs.ext4 -q -F -L root -d /guest /out/rootfs.ext4 "$(( size * 12 / 10 + 512 ))M"

# ── Firecracker + kernel ────────────────────────────────────────────────
FROM ubuntu:24.04 AS vmm
ARG FIRECRACKER_VERSION
ARG KERNEL_URL
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
RUN mkdir -p /out /tmp/fc && cd /tmp/fc \
    && curl -fsSLO "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tgz" \
    && curl -fsSLO "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tgz.sha256.txt" \
    && sha256sum -c "firecracker-${FIRECRACKER_VERSION}-x86_64.tgz.sha256.txt" \
    && tar --no-same-owner -xzf "firecracker-${FIRECRACKER_VERSION}-x86_64.tgz" \
    && cp "release-${FIRECRACKER_VERSION}-x86_64/firecracker-${FIRECRACKER_VERSION}-x86_64" /out/firecracker \
    && curl -fsSL -o /out/vmlinux "${KERNEL_URL}"

# ── Runner: one container per box ───────────────────────────────────────
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends \
        iproute2 iptables socat e2fsprogs \
    && rm -rf /var/lib/apt/lists/*
COPY --from=vmm /out/firecracker /usr/local/bin/firecracker
COPY --from=vmm /out/vmlinux /vm/vmlinux
COPY --from=rootfs /out/rootfs.ext4 /vm/rootfs.ext4
COPY entrypoint.sh /usr/local/bin/vmbox-entrypoint
RUN chmod +x /usr/local/bin/vmbox-entrypoint /usr/local/bin/firecracker
EXPOSE 7000
ENTRYPOINT ["/usr/local/bin/vmbox-entrypoint"]
