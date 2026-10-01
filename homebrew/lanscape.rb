class Lanscape < Formula
  desc "Network paths, map and service uptime in one panel"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.1"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape_1.0.1_darwin_arm64.tar.gz"
      sha256 "61fe46ddbb528371c9765b2996e2e4d06bd272af315e8310320bce4ca11d0ee7"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape_1.0.1_darwin_amd64.tar.gz"
      sha256 "6a9d90e168b22d50794ee49f46427398e4224516209cd24cac28e472776d8276"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape_1.0.1_linux_arm64.tar.gz"
      sha256 "3be021f1cedcccb9930f970d27ea297e2c4fa699ac34074e3ff7899de85848cf"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape_1.0.1_linux_amd64.tar.gz"
      sha256 "a0a6f27c892a8a494c86ee3e447c7f46be971eeada6cf9ec02dd6ee13acfb651"
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
