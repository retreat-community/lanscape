class Lanscape < Formula
  desc "Network paths, map and service uptime in one panel"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.3"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape_1.0.3_darwin_arm64.tar.gz"
      sha256 "56596d97211053c9a9a0a7cd0575743d6dfad8b51467bebf918bcf8b2b57247a"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape_1.0.3_darwin_amd64.tar.gz"
      sha256 "288b638035e62083c6c7f2eaa10dfcdd6762adecceb73e2bf6da4c68cafcefac"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape_1.0.3_linux_arm64.tar.gz"
      sha256 "fd6b48a9029fd30650b41acd2710fb2af3eec239c694d37122e4b9b78dd42803"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape_1.0.3_linux_amd64.tar.gz"
      sha256 "4776a33546c92948bf204b369b6e97f0e25a2eab8962e649629636b9dfcf0864"
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
