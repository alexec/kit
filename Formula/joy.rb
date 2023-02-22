# typed: false
# frozen_string_literal: true

# This file was generated by GoReleaser. DO NOT EDIT.
class Joy < Formula
  desc "Joyful local dev."
  homepage "https://github.com/kitproj/joy"
  version "0.0.6"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/kitproj/joy/releases/download/v0.0.6/joy_0.0.6_Darwin_x86_64.tar.gz"
      sha256 "5748d8f117d6ab04c178f39daad3901060079299f365935ff7686177220a410e"

      def install
        bin.install "joy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/kitproj/joy/releases/download/v0.0.6/joy_0.0.6_Darwin_arm64.tar.gz"
      sha256 "1846452f5c13ea452c31027d43caf71f2362e785be07f29eec6d9e3e1eedb99e"

      def install
        bin.install "joy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/kitproj/joy/releases/download/v0.0.6/joy_0.0.6_Linux_arm64.tar.gz"
      sha256 "ecbee754f949bd41381cbaed917c9cd54052fb2e895765ffaedcbfeacb06a4f3"

      def install
        bin.install "joy"
      end
    end
    if Hardware::CPU.intel?
      url "https://github.com/kitproj/joy/releases/download/v0.0.6/joy_0.0.6_Linux_x86_64.tar.gz"
      sha256 "a633655c4036f85900c967f78d4e87b3ce711e7e35ce92450313b4e1caaa65fb"

      def install
        bin.install "joy"
      end
    end
  end
end
