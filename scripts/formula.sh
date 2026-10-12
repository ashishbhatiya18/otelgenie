#!/usr/bin/env bash
# Prints the Homebrew formula for one release. It installs the program that
# the release holds, already built: no source code reaches the machine.
#
#   scripts/formula.sh VERSION > Formula/otelgenie.rb
#
# Reads dist/checksums.txt, as written by scripts/build-cli.sh.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
version="${1:?say which version}"
version="${version#v}"
base="https://github.com/ashishbhatiya18/otelgenie/releases/download/v$version"
sum() { awk -v f="$1" '$2 == f {print $1}' "$root/dist/checksums.txt" | grep . || { echo "no checksum for $1" >&2; exit 1; }; }

cat <<RUBY
class Otelgenie < Formula
  desc "From a collector endpoint to working OpenTelemetry, guided, on your own machine"
  homepage "https://github.com/ashishbhatiya18/otelgenie"
  version "$version"

  on_macos do
    on_arm do
      url "$base/otelgenie-darwin-arm64.tar.gz"
      sha256 "$(sum otelgenie-darwin-arm64.tar.gz)"
    end
    on_intel do
      url "$base/otelgenie-darwin-amd64.tar.gz"
      sha256 "$(sum otelgenie-darwin-amd64.tar.gz)"
    end
  end
  on_linux do
    on_arm do
      url "$base/otelgenie-linux-arm64.tar.gz"
      sha256 "$(sum otelgenie-linux-arm64.tar.gz)"
    end
    on_intel do
      url "$base/otelgenie-linux-amd64.tar.gz"
      sha256 "$(sum otelgenie-linux-amd64.tar.gz)"
    end
  end

  def install
    bin.install "otelgenie"
  end

  # Always the same address, so the page can be bookmarked.
  service do
    run [opt_bin/"otelgenie", "serve", "--addr", "127.0.0.1:11111"]
    keep_alive true
    log_path var/"log/otelgenie.log"
    error_log_path var/"log/otelgenie.log"
  end

  def caveats
    <<~EOS
      Say where your template is kept, fetch it, and keep the page running:
        otelgenie config template-url ADDRESS-OF-THE-TEMPLATE
        otelgenie update
        otelgenie install
      The page is then always at http://127.0.0.1:11111/
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/otelgenie version")
  end
end
RUBY
