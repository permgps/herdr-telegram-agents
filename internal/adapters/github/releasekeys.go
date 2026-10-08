package github

import (
	"crypto/ed25519"
	"fmt"
)

// releaseNamespace is the SSHSIG namespace (and allowed_signers principal)
// release statements are signed under.
const releaseNamespace = "herdr-tg-release"

// releaseSigners are the keys a release statement may be signed with. They
// must equal the key fields of scripts/signing/allowed_signers
// (TestReleaseSignersMatchScripts). The updater trusts only this compiled-in
// list, never a key file fetched from GitHub or read from a checkout.
//
// PLACEHOLDER: a development key until the owner's release key is set.
const releaseSigners = `
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPzkP3fJ0S5Nf/A72mAo93Z5KnnvNrP1STZKgC5FphmA
`

// releaseKeys parses releaseSigners. A broken constant fails every check
// closed with an error instead of panicking; TestReleaseSignersMatchScripts
// keeps it parseable.
func releaseKeys() ([]ed25519.PublicKey, error) {
	keys, err := parseAuthorizedKeys(releaseSigners)
	if err != nil {
		return nil, fmt.Errorf("release signers: %w", err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("release signers: empty")
	}
	return keys, nil
}
