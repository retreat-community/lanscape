class LanscapeAgent < Formula
  desc "Lanscape agent: network tests, discovery and checks"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.1"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape-agent_1.0.1_darwin_arm64.tar.gz"
      sha256 "ad9fa999a5aa48afdd08cc94977d7f4c762579935bd94751cb96dcd029f41b94"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape-agent_1.0.1_darwin_amd64.tar.gz"
      sha256 "34c53f88387ee79e156dcd0fc327b6fd688d922e7cd4c47cec9dfe495ca340a2"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape-agent_1.0.1_linux_arm64.tar.gz"
      sha256 "87d92afae6fcffd707926e11183f6e52878bb83a94af63313e3152320bcc5ed9"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.1/lanscape-agent_1.0.1_linux_amd64.tar.gz"
      sha256 "10ce4c195fb67fe6c248965f0da6941b7581b51ed63ca892240fa85bd08d92d0"
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
