# Release signing

- `allowed_signers`: the release keys the install scripts trust.
- `selftest.txt`, `selftest.txt.sig`, `selftest_signers`: a fixture signed
  once with a throwaway key (namespace `herdr-tg-selftest`, not kept). The
  install scripts verify it first: if it passes, this `ssh-keygen` can check
  SSHSIG signatures, so a failure on the release signature means the
  signature is bad, not that the tool is too old.

`make sign-release VERSION=X.Y.Z` signs a published release; see
`docs/development.md`.
