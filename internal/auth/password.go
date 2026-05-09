package auth

import "golang.org/x/crypto/bcrypt"

var bcryptCost = 12

// SetBcryptCostForTesting overrides the bcrypt work factor and returns a reset func.
func SetBcryptCostForTesting(cost int) func() {
	orig := bcryptCost
	bcryptCost = cost
	return func() { bcryptCost = orig }
}

func HashPassword(plain string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword verifies a plaintext password against a bcrypt hash.
func CheckPassword(hash, plain string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain))
}
