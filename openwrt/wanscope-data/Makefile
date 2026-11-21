include $(TOPDIR)/rules.mk

PKG_NAME:=wanscope-data
PKG_VERSION:=0.2.1
PKG_RELEASE:=1

PKG_LICENSE:=MIT CC-BY-4.0
PKG_MAINTAINER:=Almo7aya

include $(INCLUDE_DIR)/package.mk

# The lists are made by cmd/wanscope-data from the latest sources when CI
# builds the packages (see .github/workflows/build.yml); a local build can
# pass WANSCOPE_DATA=/path/to/dir with services.txt and owners.bin.gz.
WANSCOPE_DATA ?= $(CURDIR)/data

define Package/wanscope-data
  SECTION:=net
  CATEGORY:=Network
  TITLE:=More service names and address owners for wanscope
  URL:=https://github.com/Almo7aya/wanscope
  PKGARCH:=all
  DEPENDS:=+wanscope
endef

define Package/wanscope-data/description
  Optional lists for wanscope (about 3 MB): about 1,100 more services by
  domain, from v2fly/domain-list-community (MIT), and who announces every
  IPv4 range, from DB-IP's ASN Lite (CC BY 4.0, IP Geolocation by DB-IP,
  https://db-ip.com), to name connections nothing else names.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Configure
endef

define Build/Compile
endef

define Package/wanscope-data/install
	$(INSTALL_DIR) $(1)/usr/share/wanscope
	$(INSTALL_DATA) $(WANSCOPE_DATA)/services.txt $(1)/usr/share/wanscope/services.txt
	$(INSTALL_DATA) $(WANSCOPE_DATA)/owners.bin.gz $(1)/usr/share/wanscope/owners.bin.gz
endef

# a running wanscope picks the lists up at once
define Package/wanscope-data/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || ubus call wanscope reload >/dev/null 2>&1
exit 0
endef

$(eval $(call BuildPackage,wanscope-data))
