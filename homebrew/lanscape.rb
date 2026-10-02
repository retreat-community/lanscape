class Lanscape < Formula
  desc "Network paths, map and service uptime in one panel"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.4"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape_1.0.4_darwin_arm64.tar.gz"
      sha256 "030adfe1e7c029222654cbb598f55e446c6bcc376b3cf9e921998b775a342f7e"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape_1.0.4_darwin_amd64.tar.gz"
      sha256 "ddcf5fea329b8b0804a4aa88f2802a397cac1a8407d3a4c6d8ef002d5d801728"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape_1.0.4_linux_arm64.tar.gz"
      sha256 "96d8e92be469375b76494c48ec94eed59bff72c2d7a8104270fa25d4aeeaa876"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape_1.0.4_linux_amd64.tar.gz"
      sha256 "86e7ecb6aa7890e481e971061ea646c27fd5285601b3d412ed12186b65c0998e"
    end
  end

  def install
    bin.install "lanscape"
  end

  service do
    run [opt_bin/"lanscape", "serve", "--data-dir", var/"lanscape"]
    keep_alive true
    log_path var/"log/lanscape.log"
    error_log_path var/"log/lanscape.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/lanscape version")
  end
end
