package console

import "testing"

func TestThemeNameFromUpload(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"placard.theme.zip", "placard"},
		{"revolution.dtcg.json", "revolution"},
		{"broadside.structure.json", "broadside"},
		{"vest.zip", "vest"},
		{"tokens.json", "tokens"},
		{"", "uploaded-theme"},
	}
	for _, tt := range tests {
		if got := themeNameFromUpload("", tt.filename); got != tt.want {
			t.Errorf("themeNameFromUpload(%q) = %q, want %q", tt.filename, got, tt.want)
		}
	}
}

func TestThemeNameFromUploadPrefersExplicit(t *testing.T) {
	if got := themeNameFromUpload("chosen", "whatever.zip"); got != "chosen" {
		t.Errorf("got %q, want the explicit name", got)
	}
}
