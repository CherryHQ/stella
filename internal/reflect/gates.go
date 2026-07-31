package reflect

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const maxScoreValue = 4

var (
	secretPrivateKeyPattern  = regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`)
	secretTokenPrefixPattern = regexp.MustCompile(`(?i)\b(?:ghp_[a-z0-9_]{16,}|github_pat_[a-z0-9_]{16,}|sk-[a-z0-9_-]{16,})\b`)
	secretAssignmentPattern  = regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|access[_-]?token|refresh[_-]?token)\b\s*[:=]\s*["']?[^\s"']{8,}`)
	secretURLUserinfoPattern = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://)[^\s/@]+@`)
	secretAuthSchemePattern  = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s"'<>]+`)
	secretJWTTokenPattern    = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b`)
	longTokenPattern         = regexp.MustCompile(`[A-Za-z0-9+/=_-]{48,}`)
)

const reflectSecretRedaction = "[redacted_secret]"

func gateCandidates(inputs []CandidateGateInput, cfg CandidateGateConfig) CandidateGateResult {
	var result CandidateGateResult
	for _, input := range inputs {
		decision := CandidateGateDecision{
			Ref:    input.Ref,
			Scores: input.Scores,
		}

		if containsSecretLikeContent(input.Content) {
			decision.Reason = rejectSecretDetected
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		if !hasRequiredScores(input.Scores, cfg) {
			decision.Reason = rejectSchemaMissingField
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		if failsCoreFloor(input.Scores, cfg) {
			decision.Reason = rejectScoreFloorFailed
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		decision.NormalizedOverall = normalizedWeightedScore(input.Scores, cfg.Weights)
		if decision.NormalizedOverall < cfg.Threshold {
			decision.Reason = rejectOverallBelowThreshold
			result.Rejected = append(result.Rejected, decision)
			continue
		}

		result.Accepted = append(result.Accepted, decision)
	}

	sortGateDecisions(result.Accepted, cfg.TieBreakFields)
	if cfg.Cap > 0 && len(result.Accepted) > cfg.Cap {
		dropped := result.Accepted[cfg.Cap:]
		result.Accepted = result.Accepted[:cfg.Cap]
		for _, decision := range dropped {
			decision.Reason = rejectCapDropped
			result.Rejected = append(result.Rejected, decision)
		}
	}
	return result
}

func normalizedWeightedScore(scores map[string]int, weights map[string]float64) float64 {
	fields := make([]string, 0, len(weights))
	for field := range weights {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	var weighted, totalWeight float64
	for _, field := range fields {
		weight := weights[field]
		if weight <= 0 {
			continue
		}
		weighted += (float64(scores[field]) / maxScoreValue) * weight
		totalWeight += weight
	}
	if totalWeight == 0 {
		return 0
	}
	return weighted / totalWeight
}

func hasRequiredScores(scores map[string]int, cfg CandidateGateConfig) bool {
	required := make(map[string]struct{}, len(cfg.Weights)+len(cfg.CoreFields))
	for field := range cfg.Weights {
		required[field] = struct{}{}
	}
	for _, field := range cfg.CoreFields {
		required[field] = struct{}{}
	}
	// Extra score dimensions are schema drift, even when required fields exist.
	if len(scores) != len(required) {
		return false
	}
	for field := range required {
		score, ok := scores[field]
		if !ok || score < 0 || score > maxScoreValue {
			return false
		}
	}
	return true
}

func failsCoreFloor(scores map[string]int, cfg CandidateGateConfig) bool {
	for _, field := range cfg.CoreFields {
		if scores[field] < cfg.CoreFloor {
			return true
		}
	}
	return false
}

func sortGateDecisions(decisions []CandidateGateDecision, tieBreakFields []string) {
	sort.SliceStable(decisions, func(i, j int) bool {
		left, right := decisions[i], decisions[j]
		if left.NormalizedOverall != right.NormalizedOverall {
			return left.NormalizedOverall > right.NormalizedOverall
		}
		for _, field := range tieBreakFields {
			ls, rs := left.Scores[field], right.Scores[field]
			if ls != rs {
				return ls > rs
			}
		}
		return left.Ref < right.Ref
	})
}

func containsSecretLikeContent(content string) bool {
	_, detected := sanitizeSecretLikeContent(content)
	return detected
}

// sanitizeSecretLikeContent is shared by review-context redaction and the
// fail-closed candidate/provenance gates so their credential rules cannot drift.
func sanitizeSecretLikeContent(content string) (string, bool) {
	if content == "" {
		return "", false
	}

	sanitized := content
	detected := false
	replace := func(pattern *regexp.Regexp, replacement string) {
		if !pattern.MatchString(sanitized) {
			return
		}
		detected = true
		sanitized = pattern.ReplaceAllString(sanitized, replacement)
	}

	replace(secretURLUserinfoPattern, "$1"+reflectSecretRedaction+"@")
	replace(secretAuthSchemePattern, "$1 "+reflectSecretRedaction)
	replace(secretJWTTokenPattern, reflectSecretRedaction)
	replace(secretPrivateKeyPattern, reflectSecretRedaction)
	replace(secretTokenPrefixPattern, reflectSecretRedaction)
	replace(secretAssignmentPattern, reflectSecretRedaction)
	sanitized = longTokenPattern.ReplaceAllStringFunc(sanitized, func(token string) string {
		if !looksHighEntropyToken(token) {
			return token
		}
		detected = true
		return reflectSecretRedaction
	})
	return sanitized, detected
}

func looksHighEntropyToken(token string) bool {
	if len(token) < 48 {
		return false
	}
	var hasLower, hasUpper, hasDigit bool
	unique := make(map[rune]struct{}, len(token))
	for _, r := range token {
		unique[r] = struct{}{}
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		}
	}
	if !hasLower || !hasUpper || !hasDigit {
		return false
	}
	return float64(len(unique))/float64(len([]rune(token))) > 0.35 && !strings.Contains(token, "...")
}
