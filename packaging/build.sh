#!/usr/bin/env bash
# Сборка SPK-пакетов для Synology DSM 7.
#
#   packaging/build.sh [версия] [бренд ...]
#
# Результат: dist/<Пакет>-<версия>.spk (универсальный пакет arch=noarch с бинарниками
# для x86_64, arm64 и armv7; нужный выбирается при запуске по `uname -m`).
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
VER="${1:-1.0.0}"
shift || true
BUILD_NUM="0001"
# UI_MODE=window (по умолчанию): окно внутри DSM, вход через учётную запись DSM.
# UI_MODE=tab: запасной вариант — пункт меню открывает порт службы в новой вкладке
# (нужен WEB_ADDR в service.conf, вход по паролю).
UI_MODE="${UI_MODE:-window}"
DIST="$ROOT/dist"
WORK="$ROOT/build"

command -v go >/dev/null || { echo "нужен Go" >&2; exit 1; }
rm -rf "$WORK"
mkdir -p "$DIST" "$WORK"

# --- чтение brands.conf: переменные вида B_<бренд>_<ключ> ---------------------
declare -A B
brands=()
section=""
while IFS= read -r line || [ -n "$line" ]; do
	case "$line" in
	"" | "#"*) continue ;;
	"["*"]")
		section="${line#[}"
		section="${section%]}"
		brands+=("$section")
		;;
	*=*) B["$section.${line%%=*}"]="${line#*=}" ;;
	esac
done <"$ROOT/packaging/brands.conf"

if [ "$#" -gt 0 ]; then brands=("$@"); fi

# BSD sed (macOS) не принимает -i без аргумента-расширения, поэтому пишем во временный файл.
subst() { # subst <файл> <бренд>
	local f="$1" b="$2"
	sed \
		-e "s|@PKG@|${B[$b.pkg]}|g" \
		-e "s|@BIN@|${B[$b.bin]}|g" \
		-e "s|@DISPLAY@|${B[$b.display]}|g" "$f" >"$f.tmp" && cat "$f.tmp" >"$f" && rm "$f.tmp"
}

build_arch() { # build_arch <бренд> <goarch> <goarm|""> <суффикс> <куда>
	local b="$1" goarch="$2" goarm="$3" suffix="$4" dest="$5"
	CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" go build -trimpath \
		-ldflags "-s -w -X nascloud/internal/version.Version=$VER -X nascloud/internal/version.Repo=${UPDATE_REPO:-} -X nascloud/internal/version.PubKey=${UPDATE_PUBKEY:-} -X nascloud/internal/version.Pkg=${B[$b.pkg]}" \
		-o "$dest/${B[$b.bin]}-$suffix" "./cmd/${B[$b.cmd]}"
}

for b in "${brands[@]}"; do
	pkg="${B[$b.pkg]}"
	echo "==> $pkg $VER"
	stage="$WORK/$b"
	pkgroot="$stage/package"
	spkroot="$stage/spk"
	mkdir -p "$pkgroot/bin" "$pkgroot/ui/images" "$spkroot/scripts" "$spkroot/conf"

	# 1. бинарники
	build_arch "$b" amd64 "" amd64 "$pkgroot/bin"
	build_arch "$b" arm64 "" arm64 "$pkgroot/bin"
	build_arch "$b" arm 7 armv7 "$pkgroot/bin"
	chmod 755 "$pkgroot"/bin/*

	# 2. иконки
	go run ./packaging/tools/mkicon -out "$stage/icons" -c1 "${B[$b.c1]}" -c2 "${B[$b.c2]}"
	for s in 16 24 32 48 64 72 256; do
		cp "$stage/icons/icon_$s.png" "$pkgroot/ui/images/icon_$s.png"
	done
	# Свои иконки: положите icon_{16,24,32,48,64,72,256}.png в packaging/icons/<бренд>/
	# — они заменят сгенерированные (можно часть размеров).
	if [ -d "$ROOT/packaging/icons/$b" ]; then
		for s in 16 24 32 48 64 72 256; do
			[ -f "$ROOT/packaging/icons/$b/icon_$s.png" ] &&
				cp "$ROOT/packaging/icons/$b/icon_$s.png" "$stage/icons/icon_$s.png" &&
				cp "$stage/icons/icon_$s.png" "$pkgroot/ui/images/icon_$s.png"
		done
	fi
	cp "$stage/icons/icon_64.png" "$spkroot/PACKAGE_ICON.PNG"
	cp "$stage/icons/icon_256.png" "$spkroot/PACKAGE_ICON_256.PNG"

	# 3. ui: CGI-мост (index.cgi) и ui/config
	render_ui() { # render_ui <шаблон> <выход>
		sed -e "s|@PKG@|$pkg|g" -e "s|@BIN@|${B[$b.bin]}|g" -e "s|@DISPLAY@|${B[$b.display]}|g" "$1" >"$2"
	}
	render_ui packaging/ui/index.cgi.in "$pkgroot/ui/index.cgi"
	chmod 755 "$pkgroot/ui/index.cgi"
	if [ "$UI_MODE" = "tab" ]; then
		# запасной вариант: пункт меню открывает порт службы в новой вкладке;
		# порт регистрируется в списке встроенных приложений брандмауэра DSM (файл .sc)
		mkdir -p "$pkgroot/port_conf"
		cat >"$pkgroot/port_conf/$pkg.sc" <<EOF
[$pkg]
title="${B[$b.en_title]}"
desc="${B[$b.en_desc]}"
port_forward="no"
dst.ports="${B[$b.web_port]}/tcp"
EOF
		cat >"$pkgroot/ui/config" <<EOF
{
  ".url": {
    "${B[$b.appid]}": {
      "type": "url",
      "title": "${B[$b.display]}",
      "desc": "${B[$b.display]}",
      "icon": "images/icon_{0}.png",
      "protocol": "http",
      "port": "${B[$b.web_port]}",
      "url": "/",
      "allUsers": false,
      "grantPrivilege": "local",
      "advanceGrant": true
    }
  }
}
EOF
	else
		# окно DSM: запись type=url с относительным адресом внутри пакета (официальный способ,
		# DSM открывает такую страницу во всплывающем окне рабочего стола, на адресе самого DSM)
		cat >"$pkgroot/ui/config" <<EOF
{
  ".url": {
    "${B[$b.appid]}": {
      "type": "url",
      "title": "${B[$b.display]}",
      "desc": "${B[$b.display]}",
      "icon": "images/icon_{0}.png",
      "url": "/webman/3rdparty/$pkg/index.cgi?r=%2F",
      "allUsers": false,
      "grantPrivilege": "local",
      "advanceGrant": true
    }
  }
}
EOF
	fi

	# 4. package.tgz
	tar --owner=0 --group=0 --numeric-owner -C "$pkgroot" -czf "$spkroot/package.tgz" .

	# 5. скрипты и привилегии
	cp packaging/scripts/start-stop-status "$spkroot/scripts/start-stop-status"
	cp packaging/scripts/postinst "$spkroot/scripts/postinst"
	for n in preinst preuninst postuninst preupgrade postupgrade; do
		cp packaging/scripts/noop "$spkroot/scripts/$n"
	done
	for f in "$spkroot"/scripts/*; do subst "$f" "$b"; chmod 755 "$f"; done
	cat >"$spkroot/conf/privilege" <<'EOF'
{
  "defaults": {
    "run-as": "package"
  }
}
EOF
	web_default=""
	if [ "$UI_MODE" = "tab" ]; then
		web_default=":${B[$b.web_port]}"
	fi

	if [ "$UI_MODE" = "tab" ]; then
		printf '{\n  "port-config": {\n    "protocol-file": "port_conf/%s.sc"\n  }\n}\n' "$pkg" >"$spkroot/conf/resource"
	fi
	sed "s|@WEB_DEFAULT@|$web_default|g" "$spkroot/scripts/start-stop-status" >"$spkroot/scripts/start-stop-status.tmp" &&
		cat "$spkroot/scripts/start-stop-status.tmp" >"$spkroot/scripts/start-stop-status" &&
		rm "$spkroot/scripts/start-stop-status.tmp"

	# 6. INFO
	sum="$(md5sum "$spkroot/package.tgz" | cut -d' ' -f1)"
	admin_lines=""
	if [ "$UI_MODE" = "tab" ]; then
		admin_lines="adminprotocol=\"http\"
adminport=\"${B[$b.web_port]}\"
adminurl=\"/\""
	fi
	cat >"$spkroot/INFO" <<EOF
package="$pkg"
version="$VER-$BUILD_NUM"
os_min_ver="7.0-40000"
description="${B[$b.desc]}"
displayname="${B[$b.display]}"
arch="noarch"
maintainer="Pavel"
maintainer_url="https://pirondev.rbel.info/"
dsmuidir="ui"
dsmappname="${B[$b.appid]}"
${admin_lines}
startable="yes"
silent_install="no"
silent_upgrade="yes"
silent_uninstall="yes"
checksum="$sum"
EOF


	# 7. сборка .spk (обычный tar)
	spk="$DIST/$pkg-$VER.spk"
	rm -f "$spk"
	tar --owner=0 --group=0 --numeric-owner -C "$spkroot" -cf "$spk" \
		INFO PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG package.tgz conf scripts
	echo "    $spk ($(du -h "$spk" | cut -f1))"
done

# Подпись релиза для проверки обновлений: SHA256SUMS и SHA256SUMS.sig (нужен UPDATE_SIGN_KEY).
if [ -n "${UPDATE_SIGN_KEY:-}" ]; then
	(cd "$DIST" && shasum -a 256 ./*.spk | sed 's|\./||' >SHA256SUMS) &&
		go run ./packaging/tools/sign sign "$UPDATE_SIGN_KEY" "$DIST/SHA256SUMS" &&
		echo "подпись: $DIST/SHA256SUMS.sig"
fi
