package dia

import "strings"

// NormalizeEmail is shared by account identity and challenge issuance.
func NormalizeEmail(value string) (string, error) { return normalizeEmail(value) }

// NormalizePhoneNumber preserves Dia's US default for ten-digit national input;
// international numbers require an explicit country code.
func NormalizePhoneNumber(value string) (string, error) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && b.Len() == 0:
			b.WriteRune(r)
		case r == ' ' || r == '(' || r == ')' || r == '-' || r == '.':
		default:
			return "", ErrInvalid
		}
	}
	normalized := b.String()
	if len(normalized) == 10 && !strings.HasPrefix(normalized, "+") {
		normalized = "+1" + normalized
	} else if len(normalized) == 11 && normalized[0] == '1' {
		normalized = "+" + normalized
	}
	if len(normalized) < 8 || len(normalized) > 16 || normalized[0] != '+' || normalized[1] == '0' {
		return "", ErrInvalid
	}
	return normalized, nil
}
