package version

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
)

func PublicKey() (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(`
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEELCVRLwbXOo6XBufBeAxTxPpFocY
xY2WzejUi+qwuxsEEbVkB3x1SE2z5c7pkifG+kZJ3tLj6Iq++CJmCRpR9A==
-----END PUBLIC KEY-----`))
	if block == nil {
		return nil, errors.New("failed to decode PEM block")
	}

	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("Failed to parse public key: " + err.Error())
	}
	pubKey, ok := pub.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("PublicKey is not ECDSA")
	}
	return pubKey, nil
}
