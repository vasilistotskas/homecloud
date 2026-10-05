package verify

import "testing"

func TestRequiredTiers(t *testing.T) {
	tests := []struct {
		media, filename  string
		structure, probe bool
	}{
		{"photo", "IMG_1.HEIC", false, false},
		{"live_photo_image", "IMG_1.HEIC", false, false},
		{"video", "IMG_1.MOV", true, true},
		{"video", "IMG_1.mp4", true, true},
		{"video", "IMG_1.m4v", true, true},
		{"live_photo_video", "IMG_1_HEVC.MOV", true, true},
		{"video", "IMG_0272.GIF", false, true},
		{"video", "IMG_1", false, true},
	}
	for _, tt := range tests {
		s, p := RequiredTiers(tt.media, tt.filename)
		if s != tt.structure || p != tt.probe {
			t.Errorf("RequiredTiers(%q, %q) = %v, %v; want %v, %v", tt.media, tt.filename, s, p, tt.structure, tt.probe)
		}
	}
}
