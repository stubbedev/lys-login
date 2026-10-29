#!/bin/sh
set -eu

OUT=/run/lys-distro-vm

{
	echo "$(id -un) ${XDG_SESSION_TYPE} ${XDG_SESSION_CLASS} ${XDG_SEAT} ${XDG_VTNR}"
	loginctl show-session "${XDG_SESSION_ID}" -p Name -p Class -p Type -p Seat -p VTNr
} >"${OUT}/session.tmp"
mv "${OUT}/session.tmp" "${OUT}/session"

systemctl --user start lys-secrets.service
for _ in $(seq 60); do
	busctl --user get-property org.freedesktop.secrets /org/freedesktop/secrets/collection/login \
		org.freedesktop.Secret.Collection Locked >"${OUT}/keyring.tmp" 2>&1 || true
	mv "${OUT}/keyring.tmp" "${OUT}/keyring"
	[ "$(cat "${OUT}/keyring")" = "b false" ] && break
	sleep 0.5
done
sleep 3
