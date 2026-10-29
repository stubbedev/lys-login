#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
	echo "Usage: $0 <directory with the archive, packaging/ and scripts/>" >&2
	exit 1
fi

LOGIN_USER=alice
PASSWORD=lys-distro-vm
OUT=/run/lys-distro-vm
GREETER=/usr/local/bin/lys-distro-vm-greeter
SESSION=/usr/local/bin/lys-distro-vm-session
UNIT=/usr/lib/systemd/system/lys-login.service

fail() {
	echo "distro-vm: $*" >&2
	journalctl -b --no-pager -n 300 | grep -v "the greeter ended\|exit status 1$" >&2 || true
	exit 1
}

WORK="$(mktemp -d)"
cp -r "$1/." "${WORK}/"
cd "${WORK}"

ARCHIVE="$(echo lys-login-*-linux.tar.gz)"
VERSION="${ARCHIVE#lys-login-}"
VERSION="${VERSION%-*-linux.tar.gz}"

DISTRO_ID="$(sh -c '. /etc/os-release && echo "${ID}"')"
DISTRO="$(sh -c '. /etc/os-release && echo "${PRETTY_NAME}"')"
echo "distro-vm: ${DISTRO}"
case "${DISTRO_ID}" in
debian)
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -q
	apt-get install -y -q --no-install-recommends build-essential debhelper dpkg-dev pamtester dbus-user-session
	FORMAT=deb
	BUILT=debbuild/DEBS
	;;
fedora)
	dnf -y -q --setopt=install_weak_deps=False install rpm-build systemd-rpm-macros pamtester
	FORMAT=rpm
	BUILT=rpmbuild/RPMS
	;;
*) fail "no packages for ${DISTRO_ID}" ;;
esac

scripts/ci/build-packages.sh "${ARCHIVE}" "${VERSION}" "${FORMAT}"
PACKAGE="$(find "${BUILT}" -name "*.${FORMAT}")"
[[ -n ${PACKAGE} ]] || fail "build-packages.sh built no .${FORMAT} under ${BUILT}"
scripts/ci/smoke-test.sh "${PACKAGE}"

ARCH="$(uname -m)"
(
	cd lys-secrets
	scripts/ci/build-archive.sh bin "${VERSION}" "${ARCH}"
	scripts/ci/build-packages.sh "lys-secrets-${VERSION}-${ARCH}-linux.tar.gz" "${VERSION}" "${FORMAT}"
)
SECRETS_PACKAGE="$(find "lys-secrets/${BUILT}" -name "*.${FORMAT}")"
[[ -n ${SECRETS_PACKAGE} ]] || fail "lys-secrets' build-packages.sh built no .${FORMAT} under lys-secrets/${BUILT}"
case "${FORMAT}" in
deb) apt-get install -y -q --no-install-recommends "./${SECRETS_PACKAGE}" ;;
rpm) dnf -y -q --setopt=install_weak_deps=False install "./${SECRETS_PACKAGE}" ;;
esac

useradd -m "${LOGIN_USER}"
echo "${LOGIN_USER}:${PASSWORD}" | chpasswd
install -d -m 1777 "${OUT}"
printf '%s\n%s\n' "${LOGIN_USER}" "${PASSWORD}" >"${OUT}/credentials"
install -m 0755 scripts/distro-vm/greeter.py "${GREETER}"
install -m 0755 scripts/distro-vm/session.sh "${SESSION}"

exec_start="$(sed -n "s|^\(ExecStart=.* --compositor \)/usr/bin/lys-wm |\1${GREETER} |p" "${UNIT}")"
[[ -n ${exec_start} ]] || fail "${UNIT} has no ExecStart with --compositor /usr/bin/lys-wm"
install -d /etc/systemd/system/lys-login.service.d
printf '[Service]\nExecStart=\n%s\n' "${exec_start}" >/etc/systemd/system/lys-login.service.d/distro-vm.conf
systemctl daemon-reload
systemctl start lys-login.service

for _ in $(seq 120); do
	[[ -s ${OUT}/session ]] && break
	sleep 1
done
[[ -s ${OUT}/session ]] || fail "no session started; the greeter saw: $(cat "${OUT}/greeter" 2>/dev/null)"
echo "distro-vm: the greeter saw:"
cat "${OUT}/greeter"
echo "distro-vm: the session saw:"
cat "${OUT}/session"

grep -qx 'wrong-password {"type": "error", "error_type": "auth_error", .*' "${OUT}/greeter" ||
	fail "a wrong password was not an authentication error"
grep -qx 'password {"type": "success"}' "${OUT}/greeter" || fail "the right password did not authenticate"
[[ "$(head -n 1 "${OUT}/session")" == "${LOGIN_USER} wayland user seat0 1" ]] ||
	fail "the session ran with $(head -n 1 "${OUT}/session")"
for want in "Name=${LOGIN_USER}" Class=user Type=wayland Seat=seat0 VTNr=1; do
	grep -qx "${want}" "${OUT}/session" || fail "logind's session lacks ${want}"
done

for _ in $(seq 60); do
	[[ -e ${OUT}/greeter-again ]] && break
	sleep 1
done
[[ -e ${OUT}/greeter-again ]] || fail "the greeter did not come back after the session"
loginctl list-sessions --no-legend | grep -q lys-greeter || fail "logind has no session for the greeter"

[[ "$(cat "${OUT}/keyring" 2>/dev/null)" == "b false" ]] ||
	fail "the login did not unlock lys-secrets' login collection: $(cat "${OUT}/keyring" 2>/dev/null)"
[[ -d "/home/${LOGIN_USER}/.local/share/lys-secrets" ]] || fail "lys-secrets keeps no collection for ${LOGIN_USER}"

if echo wrong-password | runuser -u "${LOGIN_USER}" -- pamtester lys-shell "${LOGIN_USER}" authenticate; then
	fail "lys-shell accepted a wrong password"
fi
echo "${PASSWORD}" | runuser -u "${LOGIN_USER}" -- pamtester lys-shell "${LOGIN_USER}" authenticate ||
	fail "lys-shell refused ${LOGIN_USER}'s password"

if journalctl -b --no-pager | grep -F 'avc:  denied'; then
	fail "SELinux denied the above"
fi

echo "distro-vm: ${DISTRO}: logged in through lys-login, which unlocked lys-secrets, and unlocked through lys-shell"
