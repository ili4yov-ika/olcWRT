include $(TOPDIR)/rules.mk

PKG_NAME:=olcwrt
PKG_VERSION:=1.0.0
PKG_RELEASE:=1

PKG_LICENSE:=WTFPL
PKG_MAINTAINER:=olcWRT contributors

PKG_BUILD_DEPENDS:=golang/host
PKG_BUILD_PARALLEL:=1
PKG_BUILD_FLAGS:=no-mips16

GO_PKG:=olcwrt
GO_PKG_LDFLAGS:=-s -w
GO_PKG_LDFLAGS_X:=main.version=$(PKG_VERSION)

include $(INCLUDE_DIR)/package.mk
include $(TOPDIR)/feeds/packages/lang/golang/golang-package.mk

define Package/olcwrt
  SECTION:=net
  CATEGORY:=Network
  SUBMENU:=VPN
  TITLE:=olcRTC client for OpenWrt
  URL:=https://github.com/openlibrecommunity/olcrtc
  DEPENDS:=$(GO_ARCH_DEPENDS) +olcrtc +firewall4 +kmod-nft-nat +ca-bundle
endef

define Package/olcwrt/description
  olcWRT runs the olcRTC client (an encrypted TCP-over-WebRTC tunnel that
  looks like a video call on Jitsi, Yandex Telemost or WB Stream) on the
  router and routes LAN traffic through it: transparent TCP proxy, DNS
  through the tunnel, domain and IP lists, subscriptions and failover.
endef

define Package/olcwrt/conffiles
/etc/config/olcwrt
/etc/olcwrt/proxy.lst
/etc/olcwrt/bypass.lst
endef

define Build/Prepare
	$(CP) ./src/. $(PKG_BUILD_DIR)/
endef

define Package/olcwrt/install
	$(call GoPackage/Package/Install/Bin,$(1))
	$(INSTALL_DIR) $(1)/etc/init.d $(1)/etc/config $(1)/etc/olcwrt
	$(INSTALL_BIN) ./files/olcwrt.init $(1)/etc/init.d/olcwrt
	$(INSTALL_CONF) ./files/olcwrt.config $(1)/etc/config/olcwrt
	$(INSTALL_DATA) ./files/proxy.lst $(1)/etc/olcwrt/proxy.lst
	$(INSTALL_DATA) ./files/bypass.lst $(1)/etc/olcwrt/bypass.lst
	$(INSTALL_DIR) $(1)/lib/upgrade/keep.d
	$(INSTALL_DATA) ./files/olcwrt.keep $(1)/lib/upgrade/keep.d/olcwrt
endef

# The init script is enabled and started by default_postinst; it only runs
# the daemon once it is enabled in /etc/config/olcwrt.
define Package/olcwrt/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] && exit 0
/etc/init.d/olcwrt stop
/usr/bin/olcwrt cleanup
exit 0
endef

$(eval $(call GoBinPackage,olcwrt))
$(eval $(call BuildPackage,olcwrt))
