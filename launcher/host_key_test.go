package launcher

import (
	"crypto/ed25519"
	"crypto/md5"  //nolint:gosec // asserting the legacy CF fingerprint format
	"crypto/sha1" //nolint:gosec // asserting the legacy CF fingerprint format
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// testHostKey builds a deterministic SSH public key to fingerprint.
func testHostKey(t *testing.T, seed byte) ssh.PublicKey {
	t.Helper()

	seedBytes := make([]byte, ed25519.SeedSize)
	for i := range seedBytes {
		seedBytes[i] = seed
	}
	private := ed25519.NewKeyFromSeed(seedBytes)

	signer, err := ssh.NewSignerFromKey(private)
	require.NoError(t, err)
	return signer.PublicKey()
}

func TestHostKeyCallbackAcceptsEveryCFFingerprintFormat(t *testing.T) {
	key := testHostKey(t, 0x01)
	marshalled := key.Marshal()

	sha256Sum := sha256.Sum256(marshalled)
	sha1Sum := sha1.Sum(marshalled) //nolint:gosec // legacy format
	md5Sum := md5.Sum(marshalled)   //nolint:gosec // legacy format

	tests := map[string]struct {
		fingerprint  string
		expectLength int
	}{
		"base64 sha256 (advertised by current CF)": {
			fingerprint:  base64.RawStdEncoding.EncodeToString(sha256Sum[:]),
			expectLength: base64SHA256FingerprintLength,
		},
		"hex sha256": {
			fingerprint:  hex.EncodeToString(sha256Sum[:]),
			expectLength: hexSHA256FingerprintLength,
		},
		"colon-separated hex sha1": {
			fingerprint:  strings.ReplaceAll(fmt.Sprintf("% x", sha1Sum), " ", ":"),
			expectLength: hexSHA1FingerprintLength,
		},
		"colon-separated md5": {
			fingerprint:  strings.ReplaceAll(fmt.Sprintf("% x", md5Sum), " ", ":"),
			expectLength: md5FingerprintLength,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// Guard the length-based format detection: if these ever drift the
			// callback would silently pick the wrong hash.
			assert.Len(t, test.fingerprint, test.expectLength)

			err := hostKeyCallback(test.fingerprint)("ssh.example.com:2222", nil, key)
			assert.NoError(t, err)
		})
	}
}

func TestHostKeyCallbackRejectsMismatchedKey(t *testing.T) {
	expected := testHostKey(t, 0x01)
	presented := testHostKey(t, 0x02)

	sum := sha256.Sum256(expected.Marshal())
	fingerprint := base64.RawStdEncoding.EncodeToString(sum[:])

	err := hostKeyCallback(fingerprint)("ssh.example.com:2222", nil, presented)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "host key verification failed")
}

// An absent fingerprint must fail closed. Accepting any key here would let a
// man-in-the-middle collect the service credentials the tunnel carries.
func TestHostKeyCallbackRejectsEmptyFingerprint(t *testing.T) {
	err := hostKeyCallback("")("ssh.example.com:2222", nil, testHostKey(t, 0x01))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unable to verify the identity of the SSH proxy")
}

func TestHostKeyCallbackRejectsUnknownFingerprintFormat(t *testing.T) {
	err := hostKeyCallback("not-a-real-fingerprint")("ssh.example.com:2222", nil, testHostKey(t, 0x01))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported host key fingerprint format")
}
