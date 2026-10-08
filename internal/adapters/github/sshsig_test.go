package github

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/binary"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/permgps/herdr-telegram-agents/internal/testkit"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sshsig", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The fixture was made by real ssh-keygen (see testdata/sshsig/README), so
// this pins compatibility with OpenSSH rather than with our own signer.
func TestVerifySSHSigOpenSSHFixture(t *testing.T) {
	keys, err := parseAuthorizedKeys(string(readFixture(t, "signer.pub")))
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	msg := readFixture(t, "message.txt")
	const want = "SHA256:TZA9sxcVOF20XoT+mBUla84V8HoJT9+cI0Ki2fUHzfk" // ssh-keygen -lf signer.pub
	for _, name := range []string{"message.txt.sig", "message.sha256.sig"} {
		got, err := verifySSHSig(readFixture(t, name), msg, "herdr-tg-release", keys)
		if err != nil || got != want {
			t.Fatalf("%s: fingerprint %q, err %v", name, got, err)
		}
	}
	tampered := append(bytes.Clone(msg), 'x')
	if _, err := verifySSHSig(readFixture(t, "message.txt.sig"), tampered, "herdr-tg-release", keys); err == nil {
		t.Fatal("tampered message verified")
	}
	if _, err := verifySSHSig(readFixture(t, "message.txt.sig"), msg, "other", keys); err == nil {
		t.Fatal("wrong namespace verified")
	}
}

func rawSig(pubType string, pub []byte, ns, hashAlg, sigType string, sig []byte) []byte {
	var key []byte
	key = appendString(key, []byte(pubType))
	key = appendString(key, pub)
	var s []byte
	s = appendString(s, []byte(sigType))
	s = appendString(s, sig)
	var b []byte
	b = append(b, sshsigMagic...)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = appendString(b, key)
	b = appendString(b, []byte(ns))
	b = appendString(b, nil)
	b = appendString(b, []byte(hashAlg))
	b = appendString(b, s)
	return b
}

func armor(blob []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: sshsigPEMType, Bytes: blob})
}

func TestVerifySSHSigRejects(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	msg := []byte("tag v1.0.0\n")
	keys := []ed25519.PublicKey{pub}
	good := testkit.SignSSH(priv, "herdr-tg-release", msg)
	if _, err := verifySSHSig(good, msg, "herdr-tg-release", keys); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	block, _ := pem.Decode(good)
	digest := sha512.Sum512(msg)
	validSig := ed25519.Sign(priv, signedData("herdr-tg-release", nil, []byte("sha512"), digest[:]))

	cases := map[string]struct {
		sig  []byte
		keys []ed25519.PublicKey
	}{
		"unknown key":    {good, []ed25519.PublicKey{otherPub}},
		"no keys":        {good, nil},
		"sk key type":    {armor(rawSig("sk-ssh-ed25519@openssh.com", pub, "herdr-tg-release", "sha512", keyTypeEd, validSig)), keys},
		"md5 hash":       {armor(rawSig(keyTypeEd, pub, "herdr-tg-release", "md5", keyTypeEd, validSig)), keys},
		"sig type":       {armor(rawSig(keyTypeEd, pub, "herdr-tg-release", "sha512", "ssh-rsa", validSig)), keys},
		"short key":      {armor(rawSig(keyTypeEd, pub[:31], "herdr-tg-release", "sha512", keyTypeEd, validSig)), keys},
		"truncated blob": {armor(block.Bytes[:len(block.Bytes)-3]), keys},
		"trailing bytes": {armor(append(bytes.Clone(block.Bytes), 0)), keys},
		"pem type":       {pem.EncodeToMemory(&pem.Block{Type: "SIGNATURE", Bytes: block.Bytes}), keys},
		"second block":   {append(bytes.Clone(good), good...), keys},
		"pem headers":    {pem.EncodeToMemory(&pem.Block{Type: sshsigPEMType, Headers: map[string]string{"a": "b"}, Bytes: block.Bytes}), keys},
		"not armored":    {[]byte("nothing here"), keys},
		"bad signature":  {armor(rawSig(keyTypeEd, pub, "herdr-tg-release", "sha512", keyTypeEd, make([]byte, 64))), keys},
	}
	for name, c := range cases {
		if _, err := verifySSHSig(c.sig, msg, "herdr-tg-release", c.keys); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

func TestParseAuthorizedKeys(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	keys, err := parseAuthorizedKeys("# comment\n\n" + testkit.AuthorizedKey(pub) + " someone\n")
	if err != nil || len(keys) != 1 || !keys[0].Equal(pub) {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	for _, bad := range []string{"ssh-rsa AAAA", "ssh-ed25519 !!!", "ssh-ed25519"} {
		if _, err := parseAuthorizedKeys(bad); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}
