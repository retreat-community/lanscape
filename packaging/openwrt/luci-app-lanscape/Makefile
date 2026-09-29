# LuCI page for the Lanscape agent: settings (UCI), connection status, "check from this router"
# and the last results. Plain files, no compilation; needs luci-base and rpcd-mod-ucode.
include $(TOPDIR)/rules.mk

PKG_NAME:=luci-app-lanscape
PKG_VERSION:=$(if $(LSM_VERSION),$(LSM_VERSION),0.0.0)
PKG_RELEASE:=1
PKG_LICENSE:=GPL-3.0-or-later
PKG_LICENSE_FILES:=LICENSE
PKG_MAINTAINER:=Kirill Shakhov <kirill.shakhov14@gmail.com>

include $(INCLUDE_DIR)/package.mk

define Package/luci-app-lanscape
  SECTION:=luci
  CATEGORY:=LuCI
  SUBMENU:=3. Applications
  TITLE:=LuCI support for the Lanscape agent
  URL:=https://github.com/retreat-community/lanscape
  DEPENDS:=+luci-base +rpcd-mod-ucode +lanscape-agent
  PKGARCH:=all
endef

define Package/luci-app-lanscape/description
  Lanscape agent settings, status, "check from this router" and the last results in LuCI.
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
endef

define Build/Compile
endef

define Package/luci-app-lanscape/install
	$(INSTALL_DIR) $(1)/www/luci-static/resources/view/lanscape $(1)/usr/share/luci/menu.d \
		$(1)/usr/share/rpcd/acl.d $(1)/usr/share/rpcd/ucode
	$(INSTALL_DATA) ./files/view/agent.js $(1)/www/luci-static/resources/view/lanscape/agent.js
	$(INSTALL_DATA) ./files/menu.json $(1)/usr/share/luci/menu.d/luci-app-lanscape.json
	$(INSTALL_DATA) ./files/acl.json $(1)/usr/share/rpcd/acl.d/luci-app-lanscape.json
	$(INSTALL_DATA) ./files/lanscape.uc $(1)/usr/share/rpcd/ucode/lanscape.uc
endef

define Package/luci-app-lanscape/postinst
#!/bin/sh
[ -n "$${IPKG_INSTROOT}" ] || { /etc/init.d/rpcd reload; rm -rf /tmp/luci-*; } 2>/dev/null
exit 0
endef

$(eval $(call BuildPackage,luci-app-lanscape))
