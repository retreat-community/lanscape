class LanscapeAgent < Formula
  desc "Lanscape agent: network tests, discovery and checks"
  homepage "https://github.com/retreat-community/lanscape"
  version "1.0.3"
  license "GPL-3.0-or-later"

  on_macos do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape-agent_1.0.3_darwin_arm64.tar.gz"
      sha256 "8cc9552f518e3754b56639342025634178852f36d9cefbc5f66a8f9ffc63c677"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape-agent_1.0.3_darwin_amd64.tar.gz"
      sha256 "40f0b55b2ffce0bcd575b4cd6a7d077564f55f871b70960c6e272ddb93c19207"
    end
  end
  on_linux do
    on_arm do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape-agent_1.0.3_linux_arm64.tar.gz"
      sha256 "38329ab33a20146bc4e9d65e42a697c9d9c17686a0c164277f89d0df083567ef"
    end
    on_intel do
      url "https://github.com/retreat-community/lanscape/releases/download/v1.0.3/lanscape-agent_1.0.3_linux_amd64.tar.gz"
      sha256 "853c2f82ca5fa5cd1cb0768e98f8b172e12898f32464e11641d40acf888509f9"
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
