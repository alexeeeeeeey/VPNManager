include $(TOPDIR)/rules.mk

PKG_NAME:=vpn-manager
PKG_VERSION:=$(shell cat $(CURDIR)/VERSION 2>/dev/null || echo 0.1.0)
PKG_RELEASE:=1
PKG_BUILD_DIR:=$(BUILD_DIR)/$(PKG_NAME)-$(PKG_VERSION)
PKG_BUILD_PARALLEL:=1
STRIP:=:

include $(INCLUDE_DIR)/package.mk

define Package/vpn-manager
  SECTION:=net
  CATEGORY:=Network
  TITLE:=VPN Manager for OpenWrt
  DEPENDS:=@(aarch64||x86_64) +ip-full +nftables-json +kmod-nft-tproxy +kmod-nft-queue +ca-bundle +conntrack
endef

define Package/vpn-manager/description
  Web interface and Xray supervisor for transparent LAN proxying.
  Install a compatible Xray executable before activating a profile.
endef

define Package/vpn-manager/conffiles
/etc/vpn-manager/config.json
endef

define Build/Prepare
	mkdir -p $(PKG_BUILD_DIR)
	$(CP) $(CURDIR)/vpn-manager $(PKG_BUILD_DIR)/
endef

define Build/Compile
	true
endef

define Package/vpn-manager/install
	$(INSTALL_DIR) $(1)/usr/bin $(1)/etc/init.d $(1)/lib/upgrade/keep.d
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/vpn-manager $(1)/usr/bin/vpn-manager
	$(INSTALL_BIN) $(CURDIR)/vpn-manager.init $(1)/etc/init.d/vpn-manager
	printf '%s\n' '/etc/vpn-manager/' > $(1)/lib/upgrade/keep.d/vpn-manager
endef

$(eval $(call BuildPackage,vpn-manager))
