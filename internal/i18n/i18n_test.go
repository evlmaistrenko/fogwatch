package i18n

import "testing"

func TestDetect(t *testing.T) {
	if Detect("kk") != "ru" || Detect("de") != "en" || Detect("") != "" {
		t.Fatal("Detect")
	}
}
