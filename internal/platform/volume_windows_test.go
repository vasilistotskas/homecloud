package platform

import "testing"

func TestValidateVolumeID(t *testing.T) {
	for _, bad := range []string{`E:\`, `\\?\Volume{xyz}\`, "", `\\?\Volume{b0732961-8d25-42c9-b71c-1ecef673e0b2}`} {
		if err := ValidateVolumeID(bad); err == nil {
			t.Errorf("ValidateVolumeID(%q) accepted", bad)
		}
	}
	if err := ValidateVolumeID(`\\?\Volume{b0732961-8d25-42c9-b71c-1ecef673e0b2}\`); err != nil {
		t.Errorf("valid id rejected: %v", err)
	}
}
