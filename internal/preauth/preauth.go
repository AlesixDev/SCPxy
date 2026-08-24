package preauth

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/AlesixDev/scpxy/internal/litenetlib"
)

const (
	maxUserIDLength     = 64
	maxChallengeLength  = 128
	maxRegionLength     = 8
	maxSignatureLength  = 512
	minRawPreAuthLength = 8
	userIDMaskKeep      = 4
)

var (
	ErrEmpty       = errors.New("preauth: empty connection data")
	ErrUnsupported = errors.New("preauth: unsupported layout")
)

type PreAuth struct {
	ClientType     byte
	Major          byte
	Minor          byte
	Revision       byte
	BackwardCompat bool
	BackwardRev    byte
	ChallengeID    string
	UserID         string
	Expiration     int64
	Flags          byte
	Region         string
	Signature      []byte
}

func (p *PreAuth) Version() string {
	return fmt.Sprintf("%d.%d.%d", p.Major, p.Minor, p.Revision)
}

func (p *PreAuth) MaskedUserID() string {
	if p.UserID == "" {
		return "unknown"
	}

	at := strings.LastIndex(p.UserID, "@")

	if at <= 0 {
		return maskTail(p.UserID)
	}

	return maskTail(p.UserID[:at]) + p.UserID[at:]
}

func maskTail(v string) string {
	if len(v) <= userIDMaskKeep {
		return strings.Repeat("*", len(v))
	}

	return strings.Repeat("*", len(v)-userIDMaskKeep) + v[len(v)-userIDMaskKeep:]
}

func Parse(raw []byte) (*PreAuth, error) {
	if len(raw) < minRawPreAuthLength {
		return nil, ErrEmpty
	}

	r := litenetlib.NewReader(raw)
	out := &PreAuth{}

	clientType, err := r.Byte()

	if err != nil {
		return nil, ErrUnsupported
	}

	out.ClientType = clientType

	if out.Major, err = r.Byte(); err != nil {
		return nil, ErrUnsupported
	}

	if out.Minor, err = r.Byte(); err != nil {
		return nil, ErrUnsupported
	}

	if out.Revision, err = r.Byte(); err != nil {
		return nil, ErrUnsupported
	}

	if out.BackwardCompat, err = r.Bool(); err != nil {
		return nil, ErrUnsupported
	}

	if out.BackwardRev, err = r.Byte(); err != nil {
		return nil, ErrUnsupported
	}

	if out.ChallengeID, err = r.String(); err != nil {
		return nil, ErrUnsupported
	}

	if len(out.ChallengeID) > maxChallengeLength {
		return nil, ErrUnsupported
	}

	if out.UserID, err = r.String(); err != nil {
		return nil, ErrUnsupported
	}

	if len(out.UserID) > maxUserIDLength {
		return nil, ErrUnsupported
	}

	if out.Expiration, err = r.Int64(); err != nil {
		return nil, ErrUnsupported
	}

	if out.Flags, err = r.Byte(); err != nil {
		return nil, ErrUnsupported
	}

	if out.Region, err = r.String(); err != nil {
		return nil, ErrUnsupported
	}

	if len(out.Region) > maxRegionLength {
		return nil, ErrUnsupported
	}

	if out.Signature, err = r.BytesWithLength(); err != nil {
		return nil, ErrUnsupported
	}

	if len(out.Signature) > maxSignatureLength {
		return nil, ErrUnsupported
	}

	return out, nil
}

func AppendRealIP(raw []byte, ip net.IP) []byte {
	w := litenetlib.NewWriterFrom(raw)
	w.PutString(ip.String())

	return w.Bytes()
}
