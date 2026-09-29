package server

// ValidateTOTP checks a TOTP code (RFC 6238). Two-factor enrolment is managed through
// the account API; see totp_otp.go for the implementation.
func ValidateTOTP(secret, code string) bool { return validateTOTP(secret, code) }
