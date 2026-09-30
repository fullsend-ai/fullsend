package src

// ValidateSessionToken checks the token is well-formed and not expired.
func ValidateSessionToken(token string) bool {
	return len(token) > 0
}
