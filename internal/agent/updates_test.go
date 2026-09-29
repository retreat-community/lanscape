package agent

import "testing"

func TestCountUpdates(t *testing.T) {
	apt := "NOTE: This is only a simulation!\nReading package lists...\nInst libssl3 [3.0.2-0ubuntu1.14] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-updates [amd64])\n" +
		"Inst openssl [3.0.2-0ubuntu1.14] (3.0.2-0ubuntu1.15 Ubuntu:22.04/jammy-updates [amd64])\nConf libssl3 (3.0.2-0ubuntu1.15)\n"
	if n := countPrefix("Inst ")(apt); n != 2 {
		t.Errorf("apt: %d", n)
	}
	dnf := "\nkernel.x86_64    6.8.9-300.fc40    updates\nopenssl-libs.x86_64    1:3.2.1-6.fc40    updates\nObsoleting Packages\ngrub2-tools.x86_64  1:2.06  updates\n"
	if n := countDNF(dnf); n != 2 {
		t.Errorf("dnf: %d", n)
	}
	if n := countAfterHeader("Installed:                                Available:\nmusl-1.2.4-r2                           < 1.2.4-r3\n"); n != 1 {
		t.Errorf("apk: %d", n)
	}
	if n := countLines("luci-app-firewall - 1 - 2\nkmod-foo - 1 - 2\n\n"); n != 2 {
		t.Errorf("opkg: %d", n)
	}
}
