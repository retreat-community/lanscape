class LanscapeAgent < Formula
  desc "Lanscape agent: network tests, discovery and checks"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.4"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape-agent_1.0.4_darwin_arm64.tar.gz"
      sha256 "e91476037223f7fbfea3c683aee04022d2d0407fa526680480dbc9608e0432d4"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape-agent_1.0.4_darwin_amd64.tar.gz"
      sha256 "b04ffe3d190e9e5594ba6b72f623b682b319c4da6d9e4cc1c047824e442ae585"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape-agent_1.0.4_linux_arm64.tar.gz"
      sha256 "535bb4fd8c0a230e043a44b151c5be71fda0b6864f31b4110b01bcbb01ff4239"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.4/lanscape-agent_1.0.4_linux_amd64.tar.gz"
      sha256 "b3dcc8cac9a5416fb41d961999b82d3d44268b7cd7c599d16fdfa6fae2cf8b43"
    end
  end

  def install
    bin.install "lanscape-agent"
  end

  service do
    run ["/bin/sh", "-c", "set -a; [ -f #{etc}/lanscape/agent.env ] && . #{etc}/lanscape/agent.env; exec #{opt_bin}/lanscape-agent run --data-dir #{var}/lanscape-agent"]
    keep_alive true
    log_path var/"log/lanscape-agent.log"
    error_log_path var/"log/lanscape-agent.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/lanscape-agent version")
  end
end
