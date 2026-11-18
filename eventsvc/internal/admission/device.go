package admission

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// Size bounds for untrusted device input. A PKIX P-256 key encodes in 91
// bytes and an ASN.1 P-256 signature in at most 72 bytes.
const (
	maxPublicKeyBytes = 256
	maxSignatureBytes = 128
)

// Domain separation labels keep a signature for one purpose from
// verifying for another.
const (
	requestLabel   = "elysium/admission-request/v0"
	sessionLabel   = "elysium/session-challenge/v0"
	challengeLabel = "elysium/challenge-request/v0"
)

// DeviceID derives the device identifier from its PKIX DER public key.
func DeviceID(publicKey []byte) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:])
}

// RequestMessage returns the bytes a device signs to request admission.
func RequestMessage(eventID string, publicKey []byte) []byte {
	return signedMessage(requestLabel, []byte(eventID), publicKey)
}

// ChallengeRequestMessage returns the bytes a device signs to request a
// challenge.
func ChallengeRequestMessage(eventID string, publicKey, nonce []byte) []byte {
	return signedMessage(challengeLabel, []byte(eventID), publicKey, nonce)
}

// SessionMessage returns the bytes a device signs to answer a challenge.
func SessionMessage(eventID string, challenge []byte) []byte {
	return signedMessage(sessionLabel, []byte(eventID), challenge)
}

// signedMessage joins a label and length-prefixed fields. Length prefixes
// keep field boundaries unambiguous.
func signedMessage(label string, fields ...[]byte) []byte {
	message := append([]byte(label), 0)
	for _, field := range fields {
		message = binary.BigEndian.AppendUint32(message, uint32(len(field)))
		message = append(message, field...)
	}
	return message
}

// parseDeviceKey decodes a PKIX DER ECDSA P-256 public key.
func parseDeviceKey(der []byte) (*ecdsa.PublicKey, error) {
	if len(der) == 0 || len(der) > maxPublicKeyBytes {
		return nil, fmt.Errorf("%w: public key size %d", ErrInvalidArgument, len(der))
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: public key: %v", ErrInvalidArgument, err)
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("%w: public key is not ECDSA P-256", ErrInvalidArgument)
	}
	return key, nil
}

// verifyDevice checks a device signature over message.
func verifyDevice(publicKey, message, signature []byte) error {
	key, err := parseDeviceKey(publicKey)
	if err != nil {
		return err
	}
	if len(signature) == 0 || len(signature) > maxSignatureBytes {
		return fmt.Errorf("%w: signature size %d", ErrBadSignature, len(signature))
	}
	digest := sha256.Sum256(message)
	if !ecdsa.VerifyASN1(key, digest[:], signature) {
		return ErrBadSignature
	}
	return nil
}
