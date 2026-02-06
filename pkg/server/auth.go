package server

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the cost factor for bcrypt hashing
// Higher values are more secure but slower (range: 4-31, recommended: 10-12)
const BcryptCost = 12

// HashPassword hashes a plaintext password using bcrypt
// Returns the hashed password as a string, or an error if hashing fails
func HashPassword(password string) (string, error) {
	// Validate password length (bcrypt has a 72-byte limit)
	if len(password) > 72 {
		return "", bcrypt.ErrPasswordTooLong
	}

	// Generate hashed password
	hashedBytes, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", err
	}

	return string(hashedBytes), nil
}

// VerifyPassword compares a plaintext password with a hashed password
// Returns nil if the password matches, or an error if it doesn't match or verification fails
func VerifyPassword(hashedPassword, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password))
}

// IsValidPassword checks if a password meets minimum security requirements
// Returns true if password is valid, false otherwise
func IsValidPassword(password string) bool {
	// Minimum password length
	if len(password) < 8 {
		return false
	}

	// Maximum password length (bcrypt limit)
	if len(password) > 72 {
		return false
	}

	return true
}

// IsValidEmail performs basic email validation
// Returns true if email is valid, false otherwise
func IsValidEmail(email string) bool {
	// Basic validation: must contain @ and domain must have .
	atIndex := strings.Index(email, "@")
	if atIndex <= 0 {
		// No @ or @ is at the beginning
		return false
	}

	// Check that there's something after @
	if atIndex >= len(email)-1 {
		return false
	}

	// Get domain part (everything after @)
	domain := email[atIndex+1:]

	// Domain must contain at least one . and have content after it
	dotIndex := strings.Index(domain, ".")
	if dotIndex <= 0 || dotIndex >= len(domain)-1 {
		// No . in domain, or . is at beginning/end of domain
		return false
	}

	return true
}
