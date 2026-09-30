package cognito

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

const resetCodeTTL = time.Hour

// issueResetCode creates a password-reset code. Like the sign-up code it is
// written to the server log, since HomeCloud sends no e-mail or SMS. The
// password chosen with it is never logged.
func (s *Service) issueResetCode(pool, username string) error {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return err
	}
	code := fmt.Sprintf("%06d", n.Int64())
	if _, err := s.modUser(pool, username, func(u *User) error {
		u.ResetCode, u.ResetCodeExpires = hashToken(code), time.Now().Add(resetCodeTTL)
		return nil
	}); err != nil {
		return err
	}
	log.Printf("cognito: password reset code for user %q in pool %s is %s (nothing is e-mailed; AdminSetUserPassword sets a password without it)", username, pool, code)
	return nil
}

// forgotPassword starts a password reset for a user who can sign in.
func (s *Service) forgotPassword(poolID, username string) (User, error) {
	key := poolID + "/forgot/" + strings.ToLower(username)
	if !s.attempt(key) {
		return User{}, core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many requests; try again later")
	}
	u, err := s.getUser(poolID, username)
	if err != nil {
		return u, err
	}
	if !u.Enabled {
		return u, core.Errf(http.StatusBadRequest, "NotAuthorizedException", "User is disabled.")
	}
	if u.Status == "UNCONFIRMED" {
		return u, invalid("Cannot reset password for the user as there is no registered/verified email or phone_number")
	}
	return u, s.issueResetCode(poolID, u.Username)
}

// confirmForgotPassword sets a new password with the code from forgotPassword.
func (s *Service) confirmForgotPassword(poolID, username, code, password string) error {
	p, err := s.pool(poolID)
	if err != nil {
		return err
	}
	key := poolID + "/reset/" + strings.ToLower(username)
	if !s.attempt(key) {
		return core.Errf(http.StatusTooManyRequests, "TooManyRequestsException", "too many failed attempts; try again later")
	}
	u, err := s.getUser(poolID, username)
	if err != nil {
		return err
	}
	if !u.Enabled {
		return core.Errf(http.StatusBadRequest, "NotAuthorizedException", "User is disabled.")
	}
	if u.ResetCode == "" || time.Now().After(u.ResetCodeExpires) {
		return core.Errf(http.StatusBadRequest, "ExpiredCodeException", "Invalid code provided, please request a code again.")
	}
	if subtle.ConstantTimeCompare([]byte(u.ResetCode), []byte(hashToken(strings.TrimSpace(code)))) != 1 {
		return core.Errf(http.StatusBadRequest, "CodeMismatchException", "Invalid verification code provided, please try again.")
	}
	if err := p.PasswordPolicy.check(password); err != nil {
		return err
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	salt, ver := newVerifier(poolID, u.Username, password)
	s.succeeded(key)
	if _, err := s.modUser(poolID, u.Username, func(x *User) error {
		if x.ResetCode != u.ResetCode { // the code was used or replaced meanwhile
			return core.Errf(http.StatusBadRequest, "ExpiredCodeException", "Invalid code provided, please request a code again.")
		}
		x.PasswordHash, x.SRPSalt, x.SRPVerifier = string(h), salt, ver
		x.ResetCode, x.Status = "", "CONFIRMED"
		return nil
	}); err != nil {
		return err
	}
	s.revokeAll(poolID, u.Username)
	return nil
}

// adminResetPassword makes the user choose a new password with a reset code
// (ForgotPassword or the code logged here) before signing in again.
func (s *Service) adminResetPassword(poolID, username string) error {
	u, err := s.getUser(poolID, username)
	if err != nil {
		return err
	}
	if u.Status == "UNCONFIRMED" {
		return invalid("User password cannot be reset in the current state.")
	}
	if _, err := s.modUser(poolID, u.Username, func(x *User) error { x.Status = "RESET_REQUIRED"; return nil }); err != nil {
		return err
	}
	s.revokeAll(poolID, u.Username)
	return s.issueResetCode(poolID, u.Username)
}
