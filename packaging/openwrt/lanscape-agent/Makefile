# Lanscape agent (Full edition). Installs the prebuilt static binary from CI
# (files/bin/lanscape-agent, built with the lanscape_small tag), nothing is compiled by the SDK.
include $(TOPDIR)/rules.mk

PKG_NAME:=lanscape-agent
PKG_VERSION:=$(if $(LSM_VERSION),$(LSM_VERSION),0.0.0)
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-or-later
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Kirill Shakhov <kirill.shakhov14@gmail.com>

include $(INCLUDE_DIR)/package.mk

define Package/lanscape-agent
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Lanscape agent (network tests, discovery and checks)
  URL:=https://github.com/retreat-community/lanscape
  DEPENDS:=+ca-bundle
endef

define Package/lanscape-agent/description
  Connects the router to a Lanscape server: interface inventory, bound network
  tests, DHCP leases and Wi-Fi clients as discovery sources, availability checks.
  On routers with little flash use the Lanscape Mini agent (lsm-agent) instead.
endef

define Package/lanscape-agent/conffiles
/etc/config/lanscape
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/lanscape-agent/install
	$(INSTALL_DIR) $(1)/usr/bin $(1)/etc/init.d $(1)/etc/config
	$(INSTALL_BIN) ./files/bin/lanscape-agent $(1)/usr/bin/lanscape-agent
	$(INSTALL_BIN) ./files/lanscape-agent.init $(1)/etc/init.d/lanscape-agent
	$(INSTALL_CONF) ./files/lanscape.config $(1)/etc/config/lanscape
endef

$(eval $(call BuildPackage,lanscape-agent))
