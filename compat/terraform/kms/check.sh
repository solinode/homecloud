#!/usr/bin/env bash
# Encrypt through the alias and decrypt the result; rotation is on.
set -euo pipefail
alias=$($TF output -raw alias)
blob=$(aws kms encrypt --key-id "$alias" --plaintext "$(printf compat | base64)" --query CiphertextBlob --output text)
plain=$(aws kms decrypt --ciphertext-blob "$blob" --query Plaintext --output text | base64 -d)
[ "$plain" = compat ] || { echo "decrypted to $plain"; exit 1; }
key=$(aws kms describe-key --key-id "$alias" --query KeyMetadata.KeyId --output text)
[ "$(aws kms get-key-rotation-status --key-id "$key" --query KeyRotationEnabled)" = true ]
echo "kms ok"
