# Lanscape Mini server. Installs the prebuilt static binary from CI (files/bin/lsm-server).
include $(TOPDIR)/rules.mk

PKG_NAME:=lsm-server
PKG_VERSION:=$(if $(LSM_VERSION),$(LSM_VERSION),0.0.0)
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-or-later
PKG_MAINTAINER:=Kirill Shakhov <kirill.shakhov14@gmail.com>

include $(INCLUDE_DIR)/package.mk

define Package/lsm-server
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Lanscape Mini server (check-all page, matrix and map)
  URL:=https://github.com/retreat-community/lanscape
endef

define Package/lsm-server/description
  Collects Lanscape Mini agents, runs "check all" over every path and serves
  a single page with the speed matrix and the network map on port 8080.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/lsm-server/install
	$(INSTALL_DIR) $(1)/usr/sbin $(1)/etc/init.d $(1)/etc/uci-defaults
	$(INSTALL_BIN) ./files/bin/lsm-server $(1)/usr/sbin/lsm-server
	$(INSTALL_BIN) ./files/lsm-server.init $(1)/etc/init.d/lsm-server
	$(INSTALL_BIN) ./files/lsm-server.defaults $(1)/etc/uci-defaults/90-lsm-server
endef

$(eval $(call BuildPackage,lsm-server))
