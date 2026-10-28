#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <package.deb|package.rpm>" >&2
	exit 1
fi

PACKAGE="$(readlink -f "$1")"

case "${PACKAGE}" in
*.deb) apt-get install -y --no-install-recommends "${PACKAGE}" ;;
*.rpm) dnf -y --setopt=install_weak_deps=False install "${PACKAGE}" ;;
*)
	echo "Not a .deb or .rpm: ${PACKAGE}" >&2
	exit 1
	;;
esac

fail() {
	echo "smoke test: $*" >&2
	exit 1
}

status=0
lys-login >/dev/null 2>&1 || status=$?
[[ ${status} -eq 2 ]] || fail "lys-login without a mode exited ${status}, want its usage (2)"

for file in /usr/lib/systemd/system/lys-login.service /etc/pam.d/lys-login /etc/pam.d/lys-shell \
	/usr/lib/sysusers.d/lys-login.conf /usr/lib/tmpfiles.d/lys-login.conf; do
	[[ -f ${file} ]] || fail "${file} is missing"
done

getent passwd lys-greeter >/dev/null || fail "the lys-greeter user does not exist"
[[ "$(stat -c '%U %a' /var/lib/lys-greeter)" == "lys-greeter 700" ]] ||
	fail "/var/lib/lys-greeter is $(stat -c '%U %a' /var/lib/lys-greeter), want lys-greeter 700"
[[ ! -e /etc/systemd/system/display-manager.service ]] || fail "installing enabled a display manager"

echo "smoke test: ${PACKAGE} installs and runs"
