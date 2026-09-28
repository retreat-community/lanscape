#!/bin/sh
# Builds Lanscape OpenWrt packages inside an official openwrt/sdk container.
# The packages only install prebuilt static binaries produced by CI.
#
# usage (inside the SDK, as the unprivileged SDK user):
#   sdk-build.sh <repo-dir> <bin-root> <out-dir> <version> <package>...
# <bin-root>/<target>/ contains lsm-agent, lsm-server, lanscape-agent for Mini/Full targets
# (target names as in mini/Makefile: linux-amd64, linux-arm64, ...).
set -eu
repo=$1
binroot=$2
out=$3
version=$4
shift 4

sdk=${SDK_DIR:-/builder}
cd "$sdk"
if [ -x ./setup.sh ] && [ ! -d staging_dir ]; then
    ./setup.sh
fi
[ -f .config ] || make defconfig > /dev/null
arch=$(sed -n 's/^CONFIG_TARGET_ARCH_PACKAGES="\(.*\)"/\1/p' .config)
case $arch in
x86_64) target=linux-amd64 ;;
i386*) target=linux-386 ;;
aarch64*) target=linux-arm64 ;;
arm_cortex-a[5789]* | arm_cortex-a15* | arm_cortex-a17*) target=linux-armv7 ;;
arm_arm1176*) target=linux-armv6 ;;
arm_*) target=linux-armv5 ;;
mipsel_*) target=linux-mipsle ;;
mips_*) target=linux-mips ;;
mips64el_*) target=linux-mips64le ;;
mips64_*) target=linux-mips64 ;;
riscv64*) target=linux-riscv64 ;;
all) target= ;;
*) echo "unsupported package arch $arch" >&2; exit 1 ;;
esac
echo "SDK package arch: $arch (binaries: ${target:-none})"

mkdir -p package/lanscape
for p in "$@"; do
    rm -rf "package/lanscape/$p"
    cp -r "$repo/packaging/openwrt/$p" "package/lanscape/$p"
    if [ -d "package/lanscape/$p/files" ]; then
        mkdir -p "package/lanscape/$p/files/bin"
        case $p in
        lsm-agent | lsm-server | lanscape-agent)
            cp "$binroot/$target/$p" "package/lanscape/$p/files/bin/$p"
            chmod 0755 "package/lanscape/$p/files/bin/$p"
            ;;
        esac
    fi
    cp "$repo/LICENSE" "package/lanscape/$p/LICENSE" 2>/dev/null || true
done
make defconfig > /dev/null
for p in "$@"; do
    make "package/lanscape/$p/compile" LSM_VERSION="$version" V=s -j1 > "/tmp/build-$p.log" 2>&1 || {
        tail -50 "/tmp/build-$p.log"
        exit 1
    }
done
mkdir -p "$out"
find bin/packages -type f \( -name '*.ipk' -o -name '*.apk' \) \
    \( -name 'lsm-*' -o -name 'lanscape-*' -o -name 'luci-app-lanscape*' \) -exec cp {} "$out/" \;
ls -l "$out"

# Feed index (unsigned; see docs/INSTALL.md for the trust options).
cd "$out"
if ls ./*.apk > /dev/null 2>&1; then
    "$sdk/staging_dir/host/bin/apk" mkndx --allow-untrusted --output packages.adb ./*.apk
fi
if ls ./*.ipk > /dev/null 2>&1; then
    "$sdk/scripts/ipkg-make-index.sh" . > Packages
    gzip -9nc Packages > Packages.gz
fi
echo "$arch" > ARCH
ls -l
