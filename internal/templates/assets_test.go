package templates

import "testing"

func TestValidAssetFilename(t *testing.T) {
	valid := []string{"logo.png", "letterhead 2026.svg", "mark-2.webp"}
	invalid := []string{"", ".", "..", "../logo.png", "assets/logo.png", `assets\logo.png`, ".hidden.svg", "logo?.png"}
	for _, filename := range valid {
		if !ValidAssetFilename(filename) {
			t.Errorf("ValidAssetFilename(%q) = false", filename)
		}
	}
	for _, filename := range invalid {
		if ValidAssetFilename(filename) {
			t.Errorf("ValidAssetFilename(%q) = true", filename)
		}
	}
}
