package tui

import (
	"bytes"
	"testing"
)

func TestEncodeUTF16LEByteOrderAndBOM(t *testing.T) {
	got := encodeUTF16LE("ab")
	want := []byte{0xff, 0xfe, 0x61, 0x00, 0x62, 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeUTF16LE(\"ab\") = % x, want % x", got, want)
	}
}

func TestEncodeUTF16LENonASCII(t *testing.T) {
	// é (U+00E9) and ✓ (U+2713) must survive LE encoding.
	got := encodeUTF16LE("hé✓")
	want := []byte{0xff, 0xfe, 0x68, 0x00, 0xe9, 0x00, 0x13, 0x27}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeUTF16LE non-ASCII = % x, want % x", got, want)
	}
}

func TestEncodeUTF16LESurrogatePairs(t *testing.T) {
	// U+20AC (€) is in the BMP; U+1F600 (😀) is a surrogate pair.
	got := encodeUTF16LE("€😀")
	want := []byte{0xff, 0xfe, 0xac, 0x20, 0x3d, 0xd8, 0x00, 0xde}
	if !bytes.Equal(got, want) {
		t.Fatalf("encodeUTF16LE surrogates = % x, want % x", got, want)
	}
}
