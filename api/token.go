package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// tokenRefreshMargin is how long before expiry a token is treated as stale.
//
// UAA access tokens are short-lived (10 minutes on cloud.gov), while a
// connect-to-service session lasts as long as the user keeps their database
// shell open. Anything the plugin does after that -- notably deleting the
// temporary service key -- therefore needs a fresh token.
const tokenRefreshMargin = 60 * time.Second

// accessTokenExpiry extracts the exp claim from a UAA access token.
//
// The token is not verified: this code is not making an authorization decision,
// it only needs to know when to ask the CF CLI for a replacement. The CF API
// remains the authority on whether a token is acceptable.
func accessTokenExpiry(accessToken string) (time.Time, error) {
	token := accessToken
	if fields := strings.SplitN(strings.TrimSpace(token), " ", 2); len(fields) == 2 {
		token = fields[1]
	}

	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return time.Time{}, errors.New("access token is not a JWT")
	}

	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(segments[1], "="))
	if err != nil {
		return time.Time{}, fmt.Errorf("access token payload is not valid base64url: %w", err)
	}

	var claims struct {
		Expiration int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("access token payload is not valid JSON: %w", err)
	}
	if claims.Expiration == 0 {
		return time.Time{}, errors.New("access token has no exp claim")
	}

	return time.Unix(claims.Expiration, 0), nil
}
