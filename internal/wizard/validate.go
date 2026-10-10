// Package wizard provides an interactive setup wizard for Mutiauk.
package wizard

import (
	"fmt"
	"net"
)

// Validator is a function that validates a string input and returns an error if invalid.
type Validator func(string) error

// Required returns a validator that ensures the value is not empty.
func Required(fieldName string) Validator {
	return func(s string) error {
		if s == "" {
			return fmt.Errorf("%s is required", fieldName)
		}
		return nil
	}
}

// MaxLength returns a validator that ensures the value does not exceed maxLen characters.
func MaxLength(maxLen int) Validator {
	return func(s string) error {
		if len(s) > maxLen {
			return fmt.Errorf("must be %d characters or less", maxLen)
		}
		return nil
	}
}

// IntRange returns a validator that parses an integer and ensures it is within the given range.
func IntRange(min, max int, fieldName string) Validator {
	return func(s string) error {
		var val int
		if _, err := fmt.Sscanf(s, "%d", &val); err != nil {
			return fmt.Errorf("invalid %s: must be a number", fieldName)
		}
		if val < min || val > max {
			return fmt.Errorf("%s must be between %d and %d", fieldName, min, max)
		}
		return nil
	}
}

// YAMLExtension validates that the string ends with .yaml or .yml extension.
func YAMLExtension() Validator {
	return func(s string) error {
		if len(s) < 5 {
			return fmt.Errorf("config file should have .yaml or .yml extension")
		}
		ext := s[len(s)-5:]
		if ext != ".yaml" && s[len(s)-4:] != ".yml" {
			return fmt.Errorf("config file should have .yaml or .yml extension")
		}
		return nil
	}
}

// IPv4CIDR validates that the string is a valid IPv4 CIDR notation.
func IPv4CIDR() Validator {
	return func(s string) error {
		ip, _, err := net.ParseCIDR(s)
		if err != nil {
			return fmt.Errorf("invalid CIDR format: %v", err)
		}
		if ip.To4() == nil {
			return fmt.Errorf("must be an IPv4 address")
		}
		return nil
	}
}

// IPv6CIDR validates that the string is a valid IPv6 CIDR notation.
func IPv6CIDR() Validator {
	return func(s string) error {
		ip, _, err := net.ParseCIDR(s)
		if err != nil {
			return fmt.Errorf("invalid CIDR format: %v", err)
		}
		if ip.To4() != nil {
			return fmt.Errorf("must be an IPv6 address")
		}
		return nil
	}
}

// CIDR validates that the string is a valid CIDR notation (IPv4 or IPv6).
func CIDR() Validator {
	return func(s string) error {
		_, _, err := net.ParseCIDR(s)
		if err != nil {
			return fmt.Errorf("invalid CIDR format: %v", err)
		}
		return nil
	}
}

// HostPort validates that the string is a valid host:port address.
func HostPort() Validator {
	return func(s string) error {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			return fmt.Errorf("invalid address format (use host:port)")
		}
		if host == "" || port == "" {
			return fmt.Errorf("both host and port are required")
		}
		return nil
	}
}

// Chain combines multiple validators into one. All validators must pass.
func Chain(validators ...Validator) Validator {
	return func(s string) error {
		for _, v := range validators {
			if err := v(s); err != nil {
				return err
			}
		}
		return nil
	}
}
