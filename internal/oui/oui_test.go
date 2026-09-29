package oui

import "testing"

func TestVendor(t *testing.T) {
	if v := Vendor("B8:27:EB:12:34:56"); v == "" {
		t.Error("Raspberry Pi prefix not found")
	}
	if v := Vendor("3c-22-fb-00-00-00"); v == "" {
		t.Error("dash notation not handled")
	}
	if v := Vendor("02:42:ac:11:00:02"); v != "" {
		t.Errorf("locally administered address has vendor %q", v)
	}
	if Vendor("zz") != "" {
		t.Error("garbage")
	}
}
