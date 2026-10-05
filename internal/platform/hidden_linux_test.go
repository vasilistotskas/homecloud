package platform

import "testing"

func TestMarkHidden(t *testing.T) {
	if err := MarkHidden("/archive/Photos/.Hidden"); err != nil {
		t.Fatal(err)
	}
	if err := MarkHidden("/archive/Photos/Hidden"); err == nil {
		t.Fatal("a name without a leading dot is not hidden on Linux")
	}
}
