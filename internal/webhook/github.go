package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

type GitHubDelivery struct {
	Event      string
	DeliveryID string
	Repository string
	Payload    json.RawMessage

	endpointID string
}

// ValidateGitHub verifies exactly one copy of each required GitHub header,
// checks the raw request bytes before JSON parsing, then applies narrow event
// and repository allowlists. No arbitrary inbound header reaches the agent.
func (s *Service) ValidateGitHub(inv Invocation, header http.Header, body []byte, policy GitHubPolicy) (GitHubDelivery, error) {
	if inv.Endpoint.Provider != ProviderGitHub || inv.githubSecret == "" || !policy.Valid() {
		return GitHubDelivery{}, ErrInvalidGitHubDelivery
	}
	signature, ok := requiredHeader(header, "X-Hub-Signature-256")
	if !ok || !verifyGitHubSignature(inv.githubSecret, body, signature) {
		return GitHubDelivery{}, ErrInvalidGitHubDelivery
	}
	event, ok := requiredHeader(header, "X-GitHub-Event")
	if !ok {
		return GitHubDelivery{}, ErrInvalidGitHubDelivery
	}
	deliveryID, ok := requiredHeader(header, "X-GitHub-Delivery")
	if !ok || len(deliveryID) > deliveryIDMaxLen {
		return GitHubDelivery{}, ErrInvalidGitHubDelivery
	}
	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Repository.FullName == "" ||
		len(payload.Repository.FullName) > deliveryIDMaxLen {
		return GitHubDelivery{}, ErrInvalidGitHubDelivery
	}
	if !contains(policy.Events, event) || !contains(policy.Repositories, payload.Repository.FullName) {
		return GitHubDelivery{}, ErrGitHubDeliveryIgnored
	}
	return GitHubDelivery{
		Event: event, DeliveryID: deliveryID, Repository: payload.Repository.FullName,
		Payload: json.RawMessage(append([]byte(nil), body...)), endpointID: inv.Endpoint.ID,
	}, nil
}

// Envelope returns deterministic JSON with external data explicitly marked
// untrusted. It is framing for usability, not a prompt-injection boundary.
func (d GitHubDelivery) Envelope() ([]byte, error) {
	return json.Marshal(struct {
		Source     string          `json:"source"`
		Trust      string          `json:"trust"`
		Event      string          `json:"event"`
		DeliveryID string          `json:"delivery_id"`
		Repository string          `json:"repository"`
		Payload    json.RawMessage `json:"payload"`
	}{
		Source: "github", Trust: "untrusted_external_data", Event: d.Event,
		DeliveryID: d.DeliveryID, Repository: d.Repository, Payload: d.Payload,
	})
}

func (s *Service) ClaimGitHubDelivery(ctx context.Context, delivery GitHubDelivery) (bool, error) {
	if delivery.endpointID == "" || delivery.DeliveryID == "" || len(delivery.DeliveryID) > deliveryIDMaxLen {
		return false, ErrInvalidGitHubDelivery
	}
	return s.store.ClaimDelivery(ctx, delivery.endpointID, ProviderGitHub, delivery.DeliveryID)
}

func (s *Service) ReleaseGitHubDelivery(ctx context.Context, delivery GitHubDelivery) (bool, error) {
	if delivery.endpointID == "" || delivery.DeliveryID == "" || len(delivery.DeliveryID) > deliveryIDMaxLen {
		return false, ErrInvalidGitHubDelivery
	}
	n, err := s.store.ReleaseDelivery(ctx, delivery.endpointID, ProviderGitHub, delivery.DeliveryID)
	return n == 1, err
}

func requiredHeader(header http.Header, name string) (string, bool) {
	var values []string
	for key, candidate := range header {
		if strings.EqualFold(key, name) {
			values = append(values, candidate...)
		}
	}
	if len(values) != 1 || values[0] == "" || values[0] != strings.TrimSpace(values[0]) {
		return "", false
	}
	return values[0], true
}

func verifyGitHubSignature(secret string, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) || len(header) != len(prefix)+sha256.Size*2 {
		return false
	}
	hexDigest := strings.TrimPrefix(header, prefix)
	if hexDigest != strings.ToLower(hexDigest) {
		return false
	}
	supplied, err := hex.DecodeString(hexDigest)
	if err != nil || len(supplied) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(supplied, mac.Sum(nil))
}

func contains(values []string, want string) bool {
	return slices.Contains(values, want)
}
