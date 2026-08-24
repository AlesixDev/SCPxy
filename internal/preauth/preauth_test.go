package preauth

import (
	"bytes"
	"net"
	"testing"

	"github.com/AlesixDev/scpxy/internal/litenetlib"
)

func buildPreAuth(userID, region string, signature []byte) []byte {
	w := litenetlib.NewWriter()
	w.PutByte(1)
	w.PutByte(14)
	w.PutByte(1)
	w.PutByte(3)
	w.PutBool(false)
	w.PutByte(0)
	w.PutString("challenge-abc")
	w.PutString(userID)
	w.PutInt64(1893456000)
	w.PutByte(7)
	w.PutString(region)
	w.PutBytesWithLength(signature)

	return w.Bytes()
}

func TestParseRoundTrip(t *testing.T) {
	signature := bytes.Repeat([]byte{0xAB}, 64)
	raw := buildPreAuth("76561198000000000@steam", "ES", signature)

	parsed, err := Parse(raw)

	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}

	if parsed.UserID != "76561198000000000@steam" {
		t.Fatalf("UserID = %q", parsed.UserID)
	}

	if parsed.Version() != "14.1.3" {
		t.Fatalf("Version = %q", parsed.Version())
	}

	if parsed.Region != "ES" {
		t.Fatalf("Region = %q", parsed.Region)
	}

	if !bytes.Equal(parsed.Signature, signature) {
		t.Fatal("the signature does not match")
	}
}

func TestMaskedUserIDKeepsPlatform(t *testing.T) {
	parsed := &PreAuth{UserID: "76561198000000000@steam"}
	masked := parsed.MaskedUserID()

	if !bytes.HasSuffix([]byte(masked), []byte("0000@steam")) {
		t.Fatalf("masked = %q", masked)
	}

	if bytes.Contains([]byte(masked), []byte("76561198")) {
		t.Fatalf("the id is not masked: %q", masked)
	}
}

func TestAppendRealIPKeepsOriginalBytes(t *testing.T) {
	raw := buildPreAuth("id@steam", "ES", []byte{1, 2, 3})
	out := AppendRealIP(raw, net.ParseIP("203.0.113.9"))

	if !bytes.HasPrefix(out, raw) {
		t.Fatal("AppendRealIP altered the original PreAuth")
	}

	r := litenetlib.NewReader(out[len(raw):])
	ip, err := r.String()

	if err != nil {
		t.Fatalf("cannot read the appended IP: %v", err)
	}

	if ip != "203.0.113.9" {
		t.Fatalf("ip = %q", ip)
	}

	if r.Remaining() != 0 {
		t.Fatalf("%d bytes left unread", r.Remaining())
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte{0x01, 0x02}); err == nil {
		t.Fatal("expected an error on truncated data")
	}

	if _, err := Parse(nil); err == nil {
		t.Fatal("expected an error on empty data")
	}
}
