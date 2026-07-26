package util

import "testing"

func TestMediaProviderKeyIsDistinctForNonASCIIAndPunctuationNames(t *testing.T) {
	tests := [][2]string{
		{"图片中转一", "图片中转二"},
		{"image relay", "image-relay"},
		{"relay@example", "relay#example"},
	}
	for _, pair := range tests {
		left := MediaProviderKey("image", pair[0])
		right := MediaProviderKey("image", pair[1])
		if left == right {
			t.Fatalf("MediaProviderKey(%q) and MediaProviderKey(%q) collided at %q", pair[0], pair[1], left)
		}
		if repeat := MediaProviderKey("image", pair[0]); repeat != left {
			t.Fatalf("MediaProviderKey(%q) is not deterministic: %q != %q", pair[0], left, repeat)
		}
	}
}
