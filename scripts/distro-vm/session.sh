#!/bin/sh
set -eu

OUT=/run/lys-distro-vm

{
	echo "$(id -un) ${XDG_SESSION_TYPE} ${XDG_SESSION_CLASS} ${XDG_SEAT} ${XDG_VTNR}"
	loginctl show-session "${XDG_SESSION_ID}" -p Name -p Class -p Type -p Seat -p VTNr
} >"${OUT}/session.tmp"
mv "${OUT}/session.tmp" "${OUT}/session"
sleep 3
