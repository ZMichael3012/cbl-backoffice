// mint signs operator tokens for the back-office e2e stand (CBL-12.1): the
// service verifies HS256 tokens with ADMIN_JWT_SECRET, the local stand has no
// issuer, so the scenario mints tokens with the same shared dev secret.
// Run from the service directory:
//
//	go run ./test/e2e/.additionals/mint -sub op-auditor -roles auditor -ttl 1h
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// main parses the flags, signs one token, and prints it to stdout.
func main() {
	subject := flag.String("sub", "op-e2e", "operator identifier (token subject)")
	rolesArg := flag.String("roles", "", "comma-separated role names")
	ttl := flag.Duration("ttl", time.Hour, "token lifetime")
	secret := flag.String("secret", os.Getenv("ADMIN_JWT_SECRET"), "HS256 signing key (defaults to ADMIN_JWT_SECRET)")
	flag.Parse()

	if *secret == "" {
		fmt.Fprintln(os.Stderr, "error: -secret or ADMIN_JWT_SECRET is required")
		os.Exit(1)
	}
	var roles []string
	if trimmed := strings.TrimSpace(*rolesArg); trimmed != "" {
		for _, role := range strings.Split(trimmed, ",") {
			roles = append(roles, strings.TrimSpace(role))
		}
	}
	claims := jwt.MapClaims{
		"sub":   *subject,
		"roles": roles,
		"iat":   time.Now().Unix(),
		"exp":   time.Now().Add(*ttl).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(*secret))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(token)
}
