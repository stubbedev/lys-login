#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
	echo "Usage: $0 <dist-dir> <version> <arch>" >&2
	exit 1
fi

DIST_DIR="$(readlink -f "$1")"
VERSION="$2"
ARCH="$3"

cd "$(dirname "$0")/../.."

STAGING="lys-login-${VERSION}-${ARCH}-linux"
rm -rf "${STAGING}"
mkdir -p "${STAGING}"
cp "${DIST_DIR}/lys-login" "${DIST_DIR}/eff_large_wordlist.NOTICE" LICENSE \
	resources/lys-login.service \
	resources/lys-login.sysusers \
	resources/lys-login.tmpfiles \
	"${STAGING}/"
cp -r resources/pam "${STAGING}/"
tar czf "${STAGING}.tar.gz" "${STAGING}"
rm -rf "${STAGING}"
