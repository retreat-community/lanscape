# Lanscape Mini agent. Installs the prebuilt static binary from CI (files/bin/lsm-agent),
# nothing is compiled by the SDK.
include $(TOPDIR)/rules.mk

PKG_NAME:=lsm-agent
PKG_VERSION:=$(if $(LSM_VERSION),$(LSM_VERSION),0.0.0)
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-or-later
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Kirill Shakhov <kirill.shakhov14@gmail.com>

include $(INCLUDE_DIR)/package.mk

define Package/lsm-agent
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Lanscape Mini agent (network path tests)
  URL:=https://github.com/retreat-community/lanscape
endef

define Package/lsm-agent/description
  Reports interfaces and runs bound TCP throughput, ping and PMTU tests
  for a Lanscape Mini or Lanscape server.
endef

define Package/lsm-agent/conffiles
/etc/config/lsm
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/lsm-agent/install
	$(INSTALL_DIR) $(1)/usr/sbin $(1)/etc/init.d $(1)/etc/config
	$(INSTALL_BIN) ./files/bin/lsm-agent $(1)/usr/sbin/lsm-agent
	$(INSTALL_BIN) ./files/lsm-agent.init $(1)/etc/init.d/lsm-agent
	$(INSTALL_CONF) ./files/lsm.config $(1)/etc/config/lsm
endef

$(eval $(call BuildPackage,lsm-agent))
