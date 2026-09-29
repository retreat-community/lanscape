#!/bin/sh
# Publishes the release packages as an apt repository, an rpm repository and Homebrew
# formulas under the Pages site (run in a Debian container with apt-utils, createrepo-c).
# usage: publish-repos.sh <assets-dir> <site-dir> <tag>
set -eu
assets=$(cd "$1" && pwd)
site=$(cd "$2" && pwd)
tag=$3
version=${tag#v}
dl="https://github.com/retreat-community/lanscape/releases/download/$tag"

# apt: one suite ("stable"), component "main"; only the current release is kept
rm -rf "$site/apt"
mkdir -p "$site/apt/pool/main"
cp "$assets"/*.deb "$site/apt/pool/main/"
cd "$site/apt"
arches=$(for f in pool/main/*.deb; do dpkg-deb -f "$f" Architecture; done | sort -u | tr '\n' ' ')
for a in $arches; do
    mkdir -p "dists/stable/main/binary-$a"
    apt-ftparchive --arch "$a" packages pool > "dists/stable/main/binary-$a/Packages"
    gzip -9kf "dists/stable/main/binary-$a/Packages"
done
apt-ftparchive -o APT::FTPArchive::Release::Origin=Lanscape -o APT::FTPArchive::Release::Label=Lanscape \
    -o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
    -o "APT::FTPArchive::Release::Architectures=$arches" -o APT::FTPArchive::Release::Components=main \
    release dists/stable > Release.tmp
mv Release.tmp dists/stable/Release

# rpm
rm -rf "$site/rpm"
mkdir -p "$site/rpm"
cp "$assets"/*.rpm "$site/rpm/"
createrepo_c -q "$site/rpm"
cat > "$site/rpm/lanscape.repo" <<REPO
[lanscape]
name=Lanscape
baseurl=https://retreat-community.github.io/lanscape/rpm
enabled=1
gpgcheck=0
repo_gpgcheck=0
REPO

# Homebrew
sum() { sha256sum "$assets/$1" | cut -d' ' -f1; }
mkdir -p "$site/homebrew"
formula() { # <class> <binary> <desc> <service-args>
    b=$2
    cat <<RUBY
class $1 < Formula
  desc "$3"
  homepage "https://github.com/retreat-community/lanscape"
  version "$version"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "$dl/${b}_${version}_darwin_arm64.tar.gz"
      sha256 "$(sum "${b}_${version}_darwin_arm64.tar.gz")"
    end
    on_intel do
      url "$dl/${b}_${version}_darwin_amd64.tar.gz"
      sha256 "$(sum "${b}_${version}_darwin_amd64.tar.gz")"
    end
  end
  on_linux do
    on_arm do
      url "$dl/${b}_${version}_linux_arm64.tar.gz"
      sha256 "$(sum "${b}_${version}_linux_arm64.tar.gz")"
    end
    on_intel do
      url "$dl/${b}_${version}_linux_amd64.tar.gz"
      sha256 "$(sum "${b}_${version}_linux_amd64.tar.gz")"
    end
  end

  def install
    bin.install "$b"
  end

  service do
    run $4
    keep_alive true
    log_path var/"log/$b.log"
    error_log_path var/"log/$b.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/$b version")
  end
end
RUBY
}
formula Lanscape lanscape "Network paths, map and service uptime in one panel" \
    '[opt_bin/"lanscape", "serve", "--data-dir", var/"lanscape"]' > "$site/homebrew/lanscape.rb"
formula LanscapeAgent lanscape-agent "Lanscape agent: network tests, discovery and checks" \
    '["/bin/sh", "-c", "set -a; [ -f #{etc}/lanscape/agent.env ] && . #{etc}/lanscape/agent.env; exec #{opt_bin}/lanscape-agent run --data-dir #{var}/lanscape-agent"]' \
    > "$site/homebrew/lanscape-agent.rb"
