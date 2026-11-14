#!/bin/sh
# Boots one devbox microVM inside this container.
#
# Env: VG_TOKEN (required), VG_VCPUS (2), VG_MEM_MIB (4096), VG_DISK (16G)
#
# Network: tap0 (172.16.0.1/30) ↔ guest eth0 (172.16.0.2), NAT out of the
# container's own interface. The guest agent's port 7000 is forwarded to the
# container's 7000, which docker publishes to the host's loopback.
set -eu

: "${VG_TOKEN:?VG_TOKEN is required}"
VCPUS="${VG_VCPUS:-2}"
MEM="${VG_MEM_MIB:-4096}"
DISK="${VG_DISK:-16G}"
GUEST=172.16.0.2
HOSTIP=172.16.0.1

mkdir -p /run/vm

# Fresh, writable copy of the root disk, grown to the requested size.
cp --sparse=always /vm/rootfs.ext4 /run/vm/rootfs.ext4
truncate -s "$DISK" /run/vm/rootfs.ext4
e2fsck -fp /run/vm/rootfs.ext4 >/dev/null 2>&1 || true
resize2fs -f /run/vm/rootfs.ext4 >/dev/null 2>&1

ip tuntap add dev tap0 mode tap
ip addr add "$HOSTIP/30" dev tap0
ip link set tap0 up
OUT=$(ip route show default | awk '{print $5; exit}')
iptables -t nat -A POSTROUTING -s "$GUEST/32" -o "$OUT" -j MASQUERADE
iptables -P FORWARD ACCEPT

# DNS for the guest: the container's resolvers unless they're loopback-only.
DNS=$(awk '/^nameserver/ && $2 !~ /^127\./ {print $2}' /etc/resolv.conf | paste -sd, -)
[ -n "$DNS" ] || DNS=1.1.1.1,8.8.8.8

cat > /run/vm/config.json <<EOF
{
  "boot-source": {
    "kernel_image_path": "/vm/vmlinux",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw init=/lib/systemd/systemd ip=$GUEST::$HOSTIP:255.255.255.252::eth0:off vanguard.token=$VG_TOKEN vanguard.dns=$DNS"
  },
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "/run/vm/rootfs.ext4", "is_root_device": true, "is_read_only": false}
  ],
  "network-interfaces": [
    {"iface_id": "eth0", "guest_mac": "06:00:AC:10:00:02", "host_dev_name": "tap0"}
  ],
  "machine-config": {"vcpu_count": $VCPUS, "mem_size_mib": $MEM}
}
EOF

# Agent port forward: container :7000 → guest :7000.
socat TCP-LISTEN:7000,fork,reuseaddr "TCP:$GUEST:7000" &

exec firecracker --no-api --config-file /run/vm/config.json
