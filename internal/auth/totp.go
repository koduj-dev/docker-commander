package auth

import (
	"bytes"
	"encoding/base64"
	"image/png"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// TOTPIssuer is the label shown in authenticator apps (Google Authenticator,
// Authy, 1Password, …) next to the account.
const TOTPIssuer = "Docker Commander"

// Enrollment holds the data needed to show a user how to add their 2FA token.
type Enrollment struct {
	Secret    string `json:"secret"`    // base32 secret, also shown for manual entry
	OtpauthURL string `json:"otpauthUrl"` // otpauth:// provisioning URI
	QRDataURI string `json:"qrDataUri"` // data:image/png;base64,... for <img src>
}

// GenerateTOTP creates a new TOTP secret for accountName and renders a QR code
// as a data URI so the frontend can display it without extra endpoints.
func GenerateTOTP(accountName string) (*Enrollment, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      TOTPIssuer,
		AccountName: accountName,
	})
	if err != nil {
		return nil, err
	}

	img, err := key.Image(220, 220)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}

	return &Enrollment{
		Secret:     key.Secret(),
		OtpauthURL: key.URL(),
		QRDataURI:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// ValidateTOTP reports whether code is currently valid for secret. A small
// skew window is allowed to tolerate clock drift between server and device.
func ValidateTOTP(code, secret string) bool {
	valid, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totp.ValidateOpts{
		Period:    30,
		Skew:      1,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && valid
}
