include $(TOPDIR)/rules.mk

PKG_NAME:=wanscope
PKG_VERSION:=0.2.1
PKG_RELEASE:=1

PKG_LICENSE:=Apache-2.0
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Almo7aya

include $(INCLUDE_DIR)/package.mk

# The daemon is a static Go binary. CI cross-compiles it for each target
# and puts it here (see .github/workflows/build.yml); a local build can
# pass WANSCOPE_BINARY=/path/to/wanscope instead.
WANSCOPE_BINARY ?= $(CURDIR)/prebuilt/wanscope

define Package/wanscope
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Bandwidth monitor per device, uplink and service
  URL:=https://github.com/Almo7aya/wanscope
  DEPENDS:=+kmod-nf-conntrack-netlink
endef

define Package/wanscope/description
  wanscope counts traffic per device, per uplink (WAN, SIM, VPN tunnel)
  and per service from conntrack and the interface counters, names it
  from DNS answers and TLS/QUIC server names, keeps live, minute, hourly
  and daily history, and can limit, schedule, cap and block devices with
  nftables. LuCI pages are in luci-app-wanscope.
endef

define Package/wanscope/conffiles
/etc/config/wanscope
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Configure
endef

define Build/Compile
endef

define Package/wanscope/install
	$(INSTALL_DIR) $(1)/usr/bin $(1)/etc/init.d $(1)/etc/config
	$(INSTALL_BIN) $(WANSCOPE_BINARY) $(1)/usr/bin/wanscope
	$(INSTALL_BIN) ./files/wanscope.init $(1)/etc/init.d/wanscope
	$(INSTALL_CONF) ./files/wanscope.config $(1)/etc/config/wanscope
endef

define Package/wanscope/prerm
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || nft delete table inet wanscope 2>/dev/null
exit 0
endef

$(eval $(call BuildPackage,wanscope))
