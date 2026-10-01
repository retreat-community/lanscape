class Lanscape < Formula
  desc "Network paths, map and service uptime in one panel"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.2"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape_1.0.2_darwin_arm64.tar.gz"
      sha256 "6a8bcd30b1f0216789030e52f41db1678446acce92246c69431f5c3ff6645312"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape_1.0.2_darwin_amd64.tar.gz"
      sha256 "34a8f2044e4095dcc7e22370326abdfd92cc1be702ac0ac5eee81ef16a79b1eb"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape_1.0.2_linux_arm64.tar.gz"
      sha256 "025746479d8f4a2e78dcd9156809fb61abb765e4843cac788b094fc87bd501b8"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape_1.0.2_linux_amd64.tar.gz"
      sha256 "d3a1b8a2697d5cd73c5a24560a1d3aa090d90444392e57624c75b611014fbbc2"
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
