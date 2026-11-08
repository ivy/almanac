package broker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ivy/almanac/deliveries"
)

// maxID is the longest identifier taken from a header, because it is stored
// and logged.
const maxID = 128

// GitHub is webhooks from a GitHub App, signed with the App's webhook secret.
// See https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries.
type GitHub struct {
	secret []byte
}

// NewGitHub is the GitHub source for a webhook secret.
func NewGitHub(secret string) *GitHub {
	return &GitHub{secret: []byte(secret)}
}

// Name is "github".
func (*GitHub) Name() string { return "github" }

// Accept verifies a delivery's signature over its exact body, then reads its
// event, delivery ID and action. The action is all the broker reads of a
// payload: routing needs it, and nothing else does.
func (g *GitHub) Accept(header http.Header, body []byte) (deliveries.Delivery, error) {
	signature := header.Get("X-Hub-Signature-256")
	if signature == "" {
		return deliveries.Delivery{}, Unsigned
	}
	if !g.verified(signature, body) {
		return deliveries.Delivery{}, BadSignature
	}
	event, ok := identifier(header.Get("X-GitHub-Event"))
	if !ok {
		return deliveries.Delivery{}, Malformed
	}
	id, ok := identifier(header.Get("X-GitHub-Delivery"))
	if !ok {
		return deliveries.Delivery{}, Malformed
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return deliveries.Delivery{}, Malformed
	}
	kind := "github." + event
	if action, ok := actionOf(payload); ok {
		kind += "." + action
	}
	return deliveries.Delivery{Source: g.Name(), DeliveryID: id, Kind: kind, Body: body}, nil
}

// verified compares signature with the HMAC of body in constant time.
func (g *GitHub) verified(signature string, body []byte) bool {
	digest, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	claimed, err := hex.DecodeString(digest)
	if err != nil {
		return false
	}
	return hmac.Equal(claimed, digestOf(g.secret, body))
}

// Sign is the X-Hub-Signature-256 header GitHub would send for body.
func Sign(secret string, body []byte) string {
	return "sha256=" + hex.EncodeToString(digestOf([]byte(secret), body))
}

func digestOf(secret, body []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return mac.Sum(nil)
}

// actionOf is a payload's action, when it is an identifier. Anything else
// routes by event alone, so a hostile action cannot shape a kind.
func actionOf(payload any) (string, bool) {
	object, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	action, ok := object["action"].(string)
	if !ok {
		return "", false
	}
	return identifier(action)
}

// identifier is text that is safe to store and log: 1 to maxID ASCII letters,
// digits, hyphens and underscores.
func identifier(text string) (string, bool) {
	if text == "" || len(text) > maxID {
		return "", false
	}
	for _, c := range []byte(text) {
		letterOrDigit := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
		if !letterOrDigit && c != '-' && c != '_' {
			return "", false
		}
	}
	return text, true
}
