#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <lys-login-VERSION-ARCH-linux.tar.gz>" >&2
	exit 1
fi

: "${DISTRO_VM_IMAGE:?the cloud image of the distribution}"
: "${DISTRO_VM_GUEST:?the directory guest.sh and the packaging scripts are in}"

ARCHIVE="$(readlink -f "$1")"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

mkdir "${WORK}/src"
cp -r "${DISTRO_VM_GUEST}/." "${ARCHIVE}" "${WORK}/src/"
chmod -R u+w "${WORK}/src"

cat >"${WORK}/user-data" <<'CLOUD'
#cloud-config
runcmd:
  - |
    mkdir -p /mnt/lys && mount -o ro /dev/disk/by-label/LYS /mnt/lys && bash /mnt/lys/scripts/distro-vm/guest.sh /mnt/lys >/dev/ttyS1 2>&1
    echo "distro-vm: exit $?" >/dev/ttyS1
    poweroff
CLOUD
echo "instance-id: lys-distro-vm" >"${WORK}/meta-data"

xorriso -as mkisofs -quiet -o "${WORK}/seed.iso" -V cidata -J -R "${WORK}/user-data" "${WORK}/meta-data"
xorriso -as mkisofs -quiet -o "${WORK}/src.iso" -V LYS -J -R "${WORK}/src"
qemu-img create -q -f qcow2 -F qcow2 -b "${DISTRO_VM_IMAGE}" "${WORK}/disk.qcow2" 12G

timeout 1800 qemu-system-x86_64 -enable-kvm -cpu host -smp 2 -m 2048 -no-reboot \
	-drive "file=${WORK}/disk.qcow2,if=virtio" \
	-drive "file=${WORK}/seed.iso,if=virtio,format=raw,readonly=on" \
	-drive "file=${WORK}/src.iso,if=virtio,format=raw,readonly=on" \
	-nic user,model=virtio-net-pci -vga std -display none \
	-serial "file:${WORK}/console.log" -serial "file:${WORK}/result.log"

tr -d '\r' <"${WORK}/result.log"
if ! tr -d '\r' <"${WORK}/result.log" | grep -qx 'distro-vm: exit 0'; then
	echo "distro-vm: the guest failed; the end of its console:" >&2
	tail -n 60 "${WORK}/console.log" >&2
	exit 1
fi
