package i18n

import "testing"

func TestDetect(t *testing.T) {
	if Detect("kk") != "ru" || Detect("de") != "en" || Detect("") != "" {
		t.Fatal("Detect")
	}
}

func TestKeysInBothLangs(t *testing.T) {
	for k := range texts["ru"] {
		if _, ok := texts["en"][k]; !ok {
			t.Errorf("%q missing in en", k)
		}
	}
	for k := range texts["en"] {
		if _, ok := texts["ru"][k]; !ok {
			t.Errorf("%q missing in ru", k)
		}
	}
	if len(AdminCommands["ru"]) != len(AdminCommands["en"]) {
		t.Error("admin menus differ")
	}
}
