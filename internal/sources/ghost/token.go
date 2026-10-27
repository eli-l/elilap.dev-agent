package ghost

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// adminToken builds the HS256 JWT the Ghost Admin API expects:
// header:{alg:HS256,typ:JWT,kid:<key id>}.{exp payload}.{hmac-sha256}.
// Ghost ignores most JWT conventions: the kid goes into the header, the
// payload carries only a 5-minute exp, and the signing input is the raw
// base64url segments.
func (c *Client) adminToken() (string, error) {
	if c.keyID == "" || len(c.keySec) == 0 {
		return "", errors.New("ghost: API key not configured")
	}
	header := base64url(`{"alg":"HS256","typ":"JWT","kid":"` + c.keyID + `"}`)
	claims := base64url(fmt.Sprintf(`{"exp":%d}`, c.now().Add(5*time.Minute).Unix()))
	signingInput := header + "." + claims
	mac := hmac.New(sha256.New, c.keySec)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return header + "." + claims + "." + sig, nil
}

// base64url encodes s as unpadded base64url, as JWT segments require.
func base64url(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}
