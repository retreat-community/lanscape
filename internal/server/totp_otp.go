package server

import (
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

func validateTOTP(secret, code string) bool {
	if code == "" {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{Period: 30, Skew: 1, Digits: otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1})
	return err == nil && ok
}

// NewTOTP creates a TOTP secret and its otpauth:// URL for enrolment.
func NewTOTP(account string) (secret, url string, err error) {
	k, err := totp.Generate(totp.GenerateOpts{Issuer: "Lanscape", AccountName: account})
	if err != nil {
		return "", "", err
	}
	return k.Secret(), k.URL(), nil
}
