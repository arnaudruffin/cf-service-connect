package launcher

import (
	"crypto/md5"  //nolint:gosec // MD5 is only used to match the CF CLI's legacy fingerprint format
	"crypto/sha1" //nolint:gosec // SHA-1 is only used to match the CF CLI's legacy fingerprint format
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Fingerprint formats advertised by CF over the years. The format is inferred
// from the length of the expected value, exactly as the CF CLI does in
// util/clissh/ssh.go, so that this plugin accepts every foundation the CLI does.
const (
	md5FingerprintLength          = 47 // "aa:bb:..." including separators
	hexSHA1FingerprintLength      = 59 // "aa:bb:..." including separators
	base64SHA256FingerprintLength = 43 // unpadded base64
	hexSHA256FingerprintLength    = 64
)

// hostKeyCallback verifies the SSH proxy's host key against the fingerprint
// advertised by the CF API root document.
//
// An empty expected fingerprint is rejected rather than skipped: the whole
// point of publishing it in the root document is to pin the proxy's identity,
// and silently accepting any key would expose credentials to a
// man-in-the-middle.
func hostKeyCallback(expectedFingerprint string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if expectedFingerprint == "" {
			return fmt.Errorf(
				"unable to verify the identity of the SSH proxy: the CF API advertised no host key fingerprint (the key presented was %q)",
				sha256Fingerprint(key, true))
		}

		var actual string
		switch len(expectedFingerprint) {
		case base64SHA256FingerprintLength:
			actual = sha256Fingerprint(key, true)
		case hexSHA256FingerprintLength:
			actual = sha256Fingerprint(key, false)
		case hexSHA1FingerprintLength:
			actual = hexSHA1Fingerprint(key)
		case md5FingerprintLength:
			actual = md5Fingerprint(key)
		default:
			return fmt.Errorf("unsupported host key fingerprint format advertised by the CF API (length %d)", len(expectedFingerprint))
		}

		// Constant-time comparison: the fingerprint is not secret, but this
		// costs nothing and keeps the comparison free of timing signal.
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expectedFingerprint)) != 1 {
			return fmt.Errorf("host key verification failed: the SSH proxy presented a key with fingerprint %q, but the CF API advertised %q",
				actual, expectedFingerprint)
		}
		return nil
	}
}

func sha256Fingerprint(key ssh.PublicKey, encodeBase64 bool) string {
	sum := sha256.Sum256(key.Marshal())
	if encodeBase64 {
		return base64.RawStdEncoding.EncodeToString(sum[:])
	}
	return hex.EncodeToString(sum[:])
}

func hexSHA1Fingerprint(key ssh.PublicKey) string {
	sum := sha1.Sum(key.Marshal()) //nolint:gosec // legacy CF fingerprint format
	return strings.ReplaceAll(fmt.Sprintf("% x", sum), " ", ":")
}

func md5Fingerprint(key ssh.PublicKey) string {
	sum := md5.Sum(key.Marshal()) //nolint:gosec // legacy CF fingerprint format
	return strings.ReplaceAll(fmt.Sprintf("% x", sum), " ", ":")
}
