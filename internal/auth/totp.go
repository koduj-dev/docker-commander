package auth

import (
	"bytes"
	"crypto/subtle"
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
	Secret     string `json:"secret"`     // base32 secret, also shown for manual entry
	OtpauthURL string `json:"otpauthUrl"` // otpauth:// provisioning URI
	QRDataURI  string `json:"qrDataUri"`  // data:image/png;base64,... for <img src>
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

// totpPeriod is the TOTP time step in seconds. A code is valid for one step,
// plus one either side to tolerate clock drift (see totpSkew).
const totpPeriod = 30

// totpSkew is how many steps either side of "now" are accepted.
const totpSkew = 1

var totpOpts = totp.ValidateOpts{
	Period:    totpPeriod,
	Skew:      totpSkew,
	Digits:    otp.DigitsSix,
	Algorithm: otp.AlgorithmSHA1,
}

// ValidateTOTP reports whether code is currently valid for secret. A small
// skew window is allowed to tolerate clock drift between server and device.
//
// Prefer MatchTOTP where a replay matters: this answers "is it valid", which
// stays true for the whole window, so the same code keeps working until it
// expires.
func ValidateTOTP(code, secret string) bool {
	valid, err := totp.ValidateCustom(code, secret, time.Now().UTC(), totpOpts)
	return err == nil && valid
}

// MatchTOTP reports whether code is valid and, if so, which time step produced
// it — so the caller can refuse to accept that step a second time.
//
// The library's own validation only answers yes/no, and "yes" holds for the whole
// ~90-second window. That makes a single observed code (shoulder-surfed, phished
// through a proxy, screenshotted by malware) spendable more than once, which is
// precisely what a one-time password is supposed to prevent.
//
// It re-derives the code for each step in the skew window and compares in
// constant time, so a wrong code leaks nothing about how wrong it was.
func MatchTOTP(code, secret string) (counter int64, ok bool) {
	now := time.Now().UTC()
	for delta := -totpSkew; delta <= totpSkew; delta++ {
		at := now.Add(time.Duration(delta*totpPeriod) * time.Second)
		want, err := totp.GenerateCodeCustom(secret, at, totpOpts)
		if err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return at.Unix() / totpPeriod, true
		}
	}
	return 0, false
}
