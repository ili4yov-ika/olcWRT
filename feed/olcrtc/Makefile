include $(TOPDIR)/rules.mk

PKG_NAME:=olcrtc
PKG_RELEASE:=1

PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/openlibrecommunity/olcrtc.git
PKG_SOURCE_DATE:=2026-09-30
PKG_SOURCE_VERSION:=f3ad8fb7c0d0fa981423f83269526fa94077c23d
PKG_MIRROR_HASH:=skip

PKG_LICENSE:=WTFPL
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=olcWRT contributors

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=github.com/openlibrecommunity/olcrtc
GO_PKG_BUILD_PKG:=github.com/openlibrecommunity/olcrtc/cmd/olcrtc
GO_PKG_LDFLAGS:=-s -w

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/olcrtc
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=olcRTC encrypted TCP-over-WebRTC tunnel
  URL:=https://github.com/openlibrecommunity/olcrtc
  DEPENDS:=$(GO_ARCH_DEPENDS) +ca-bundle
endef

define Package/olcrtc/description
  olcRTC tunnels TCP over WebRTC through public video conferencing
  services (Jitsi, Yandex Telemost, WB Stream). This package provides the
  olcrtc binary; use olcwrt / luci-app-olcwrt to run it as a client.
  Requires golang 1.26 or newer in the packages feed.
endef

$(eval $(call GoBinPackage,olcrtc))
$(eval $(call BuildPackage,olcrtc))
