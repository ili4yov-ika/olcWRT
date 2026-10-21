include $(TOPDIR)/rules.mk

LUCI_TITLE:=LuCI interface for olcWRT (olcRTC client)
LUCI_DESCRIPTION:=Web interface for olcWRT: servers, subscriptions, routing and status.
LUCI_DEPENDS:=+luci-base +olcwrt
LUCI_PKGARCH:=all

PKG_VERSION:=1.0.0
PKG_RELEASE:=1
PKG_LICENSE:=WTFPL
PKG_MAINTAINER:=olcWRT contributors

include $(TOPDIR)/feeds/luci/luci.mk

# call BuildPackage - OpenWrt buildroot signature
