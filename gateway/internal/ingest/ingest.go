// Package ingest terminates authenticated DTLS-SRTP sessions between
// broadcaster sources and the venue ingest gateway (D-005, D-006).
//
// Both ends authenticate with mutual certificates pinned by SHA-256
// fingerprint. DTLS and SRTP share one UDP 5-tuple. The package
// demultiplexes them by first byte as RFC 7983 describes. RTP payloads
// pass through unchanged to keep encoded Opus intact (D-007).
package ingest

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"

	"github.com/pion/dtls/v3"
	"github.com/pion/srtp/v3"
)

// ErrInvalidConfig reports an unusable gateway or source configuration.
var ErrInvalidConfig = errors.New("ingest: invalid configuration")

// ErrClosed reports use of a closed gateway or session.
var ErrClosed = errors.New("ingest: closed")

// errUnpinnedCertificate reports a peer certificate outside the pin set.
var errUnpinnedCertificate = errors.New("ingest: peer certificate is not pinned")

const (
	// maxDatagramBytes covers the largest possible UDP payload.
	maxDatagramBytes = 1 << 16
	// handshakeQueueDatagrams bounds buffered DTLS datagrams per peer.
	// Experimental value tracked in docs/backlog.md.
	handshakeQueueDatagrams = 64
	// replayWindowPackets matches the pion/srtp session default.
	replayWindowPackets = 64
)

// srtpProfiles lists the offered SRTP protection profiles in preference
// order. AES-CM with HMAC-SHA1-80 stays as a fallback for constrained
// broadcasters such as the later ESP32 phase.
var srtpProfiles = []dtls.SRTPProtectionProfile{
	dtls.SRTP_AEAD_AES_128_GCM,
	dtls.SRTP_AES128_CM_HMAC_SHA1_80,
}

// Fingerprint is the SHA-256 digest of a DER-encoded certificate.
type Fingerprint [sha256.Size]byte

// FingerprintSHA256 returns the SHA-256 fingerprint of a DER certificate.
func FingerprintSHA256(der []byte) Fingerprint {
	return Fingerprint(sha256.Sum256(der))
}

// hasCertificate reports whether cert holds a leaf and a private key.
func hasCertificate(cert tls.Certificate) bool {
	return len(cert.Certificate) > 0 && cert.PrivateKey != nil
}

// pinVerifier accepts only a leaf certificate whose fingerprint appears
// in pinned. Pinning replaces CA chain validation for venue devices.
func pinVerifier(pinned map[Fingerprint]struct{}) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errUnpinnedCertificate
		}
		if _, ok := pinned[FingerprintSHA256(rawCerts[0])]; !ok {
			return errUnpinnedCertificate
		}
		return nil
	}
}

// srtpContexts holds the SRTP contexts for one direction pair.
type srtpContexts struct {
	local  *srtp.Context
	remote *srtp.Context
}

// deriveSRTPContexts exports RFC 5764 keying material from a completed
// DTLS handshake and builds the local and remote SRTP contexts.
func deriveSRTPContexts(conn *dtls.Conn, isClient bool) (srtpContexts, error) {
	profile, ok := conn.SelectedSRTPProtectionProfile()
	if !ok {
		return srtpContexts{}, errors.New("ingest: no SRTP protection profile negotiated")
	}
	state, ok := conn.ConnectionState()
	if !ok {
		return srtpContexts{}, errors.New("ingest: DTLS connection state unavailable")
	}
	cfg := srtp.Config{Profile: srtp.ProtectionProfile(profile)}
	if err := cfg.ExtractSessionKeysFromDTLS(&state, isClient); err != nil {
		return srtpContexts{}, fmt.Errorf("ingest: export SRTP keys: %w", err)
	}
	local, err := srtp.CreateContext(cfg.Keys.LocalMasterKey, cfg.Keys.LocalMasterSalt, cfg.Profile)
	if err != nil {
		return srtpContexts{}, fmt.Errorf("ingest: create local SRTP context: %w", err)
	}
	remote, err := srtp.CreateContext(cfg.Keys.RemoteMasterKey, cfg.Keys.RemoteMasterSalt, cfg.Profile,
		srtp.SRTPReplayProtection(replayWindowPackets))
	if err != nil {
		return srtpContexts{}, fmt.Errorf("ingest: create remote SRTP context: %w", err)
	}
	return srtpContexts{local: local, remote: remote}, nil
}

// peerFingerprint returns the fingerprint of the authenticated peer leaf.
func peerFingerprint(conn *dtls.Conn) (Fingerprint, error) {
	state, ok := conn.ConnectionState()
	if !ok || len(state.PeerCertificates) == 0 {
		return Fingerprint{}, errors.New("ingest: peer certificate unavailable")
	}
	return FingerprintSHA256(state.PeerCertificates[0]), nil
}
