# typed: false
# frozen_string_literal: true

# Homebrew formula template for Packwright.
#
# Rendered by .github/workflows/release.yml on every `v*` tag push. The
# release workflow substitutes the placeholders below via sed, then opens
# a PR against the tap repo (bannaarr01/homebrew-packwright) with the
# rendered formula. Brew users install the TUI binary; the GUI ships as
# the signed .dmg / installer / AppImage from the GitHub Release.
#
# Placeholders (all substituted in CI; do not hand-edit):
#   @@VERSION@@              e.g. 1.2.3 (no leading v)
#   @@DARWIN_ARM64_SHA256@@  sha256 of packwright-<v>-darwin-arm64.tar.gz
#   @@DARWIN_AMD64_SHA256@@  sha256 of packwright-<v>-darwin-amd64.tar.gz
#   @@LINUX_AMD64_SHA256@@   sha256 of packwright-<v>-linux-amd64.tar.gz
#   @@LINUX_ARM64_SHA256@@   sha256 of packwright-<v>-linux-arm64.tar.gz
class Packwright < Formula
  desc "Hybrid TUI + GUI tool for scaffolding AWS infrastructure templates"
  homepage "https://github.com/bannaarr01/packwright"
  version "@@VERSION@@"
  license "Apache-2.0"

  on_macos do
    on_arm do
      url "https://github.com/bannaarr01/packwright/releases/download/v@@VERSION@@/packwright-@@VERSION@@-darwin-arm64.tar.gz"
      sha256 "@@DARWIN_ARM64_SHA256@@"
    end
    on_intel do
      url "https://github.com/bannaarr01/packwright/releases/download/v@@VERSION@@/packwright-@@VERSION@@-darwin-amd64.tar.gz"
      sha256 "@@DARWIN_AMD64_SHA256@@"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/bannaarr01/packwright/releases/download/v@@VERSION@@/packwright-@@VERSION@@-linux-amd64.tar.gz"
      sha256 "@@LINUX_AMD64_SHA256@@"
    end
    on_arm do
      url "https://github.com/bannaarr01/packwright/releases/download/v@@VERSION@@/packwright-@@VERSION@@-linux-arm64.tar.gz"
      sha256 "@@LINUX_ARM64_SHA256@@"
    end
  end

  def install
    # Each release tarball unpacks into a single directory:
    #   packwright-<version>-<os>-<arch>/{packwright, LICENSE, NOTICE, README.md}
    dir = Dir["packwright-*"].first
    cd dir if dir
    bin.install "packwright"
    doc.install "LICENSE", "NOTICE", "README.md"
  end

  test do
    # `packwright --version` prints the ldflags-injected version and exits 0,
    # confirming brew installed the intended build.
    assert_match version.to_s, shell_output("#{bin}/packwright --version")
  end
end
