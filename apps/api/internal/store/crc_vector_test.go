package store
import "testing"
func TestCRC64Vector(t *testing.T) {
	got := crc64(0, []byte("123456789"))
	if got != 0xe9c6d914c4b8d9ca {
		t.Fatalf("crc64(123456789)=%#x, want 0xe9c6d914c4b8d9ca", got)
	}
	// hello-world DUMP payload (type+len+bytes+version), CRC bytes were
	// a9 55 c0 0a a1 72 d7 f4 (little-endian) → 0xf4d772a10ac055a9.
	payload := []byte{0x00, 0x0b}
	payload = append(payload, []byte("hello world")...)
	payload = append(payload, 0x0d, 0x00)
	if got := crc64(0, payload); got != 0xf4d772a10ac055a9 {
		t.Fatalf("hello-world crc=%#x, want 0xf4d772a10ac055a9", got)
	}
}
