class LanscapeAgent < Formula
  desc "Lanscape agent: network tests, discovery and checks"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.2"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape-agent_1.0.2_darwin_arm64.tar.gz"
      sha256 "19f5ec38ce70d49d7e9e617eeb2c6ccc232ac0ab6014b19a351c621582c297b4"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape-agent_1.0.2_darwin_amd64.tar.gz"
      sha256 "f841d01a5f5bf19806ed37a903f5d83088c81642d9bd4bdfd3029232aa69a88d"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape-agent_1.0.2_linux_arm64.tar.gz"
      sha256 "ca700e6f2573c5b702844438eee5d1ad2895f4a95bdc0b1b929d4d186d14399f"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.2/lanscape-agent_1.0.2_linux_amd64.tar.gz"
      sha256 "c8f980d67cc5555ac84fea939da104d556fd2f34e4b58b628061241e65dd17dc"
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
