package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

const defaultHMACSecret = "ticketpulse_hmac_secret_2026"

func hmacSecret() []byte {
	if s := os.Getenv("HMAC_SECRET_KEY"); s != "" {
		return []byte(s)
	}
	return []byte(defaultHMACSecret)
}

// SignTicketID returns the hex-encoded HMAC-SHA256 signature for a ticket/order id. Embedded
// in each order's QR payload and re-derived at the gate scanner to authenticate scans without
// a DB round trip.
func SignTicketID(ticketID string) string {
	mac := hmac.New(sha256.New, hmacSecret())
	mac.Write([]byte(ticketID))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyTicketSignature reports whether sig is the valid HMAC-SHA256 signature for ticketID.
func VerifyTicketSignature(ticketID, sig string) bool {
	expected, err := hex.DecodeString(SignTicketID(ticketID))
	if err != nil {
		return false
	}
	given, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, given)
}
