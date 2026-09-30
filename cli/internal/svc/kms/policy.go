package kms

import (
	"strings"

	"github.com/homecloudhq/homecloud/cli/internal/httpx"
)

// Every key has a key policy (the default one grants the account's root, which
// delegates to IAM policies, as in AWS), and it is evaluated on every operation
// on the key over the native and the AWS API: an explicit Deny in it wins, a
// Principal it names directly is allowed without IAM policies, and IAM policies
// only count when the key policy delegates to the account. Grants are stored
// and listed but do not authorize requests.

// policyProvider supplies a key's policy to authorization.
func (s *Service) policyProvider(arn string) (httpx.Access, bool) {
	_, id, ok := strings.Cut(arn, ":key/")
	if !ok {
		return httpx.Access{}, false
	}
	k, err := s.resolve(id)
	if err != nil {
		return httpx.Access{}, false
	}
	return httpx.Access{Policy: s.policyOf(k), KeyPolicy: true}, true
}
