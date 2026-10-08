package github

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"strings"
)

// SSHSIG as written by `ssh-keygen -Y sign` (OpenSSH PROTOCOL.sshsig).
// Only plain ssh-ed25519 keys are accepted: the release key is a file key,
// and security-key signatures carry flags and a counter this verifier does
// not check.
const (
	sshsigMagic   = "SSHSIG"
	sshsigVersion = 1
	sshsigPEMType = "SSH SIGNATURE"
	keyTypeEd     = "ssh-ed25519"
)

var errSSHSig = errors.New("ssh signature")

// verifySSHSig checks an armored SSHSIG over message in namespace against
// keys and returns the matched key's OpenSSH fingerprint.
func verifySSHSig(armored, message []byte, namespace string, keys []ed25519.PublicKey) (string, error) {
	block, rest := pem.Decode(armored)
	if block == nil {
		return "", fmt.Errorf("%w: no armored block", errSSHSig)
	}
	if block.Type != sshsigPEMType || len(block.Headers) != 0 {
		return "", fmt.Errorf("%w: armor type %q", errSSHSig, block.Type)
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return "", fmt.Errorf("%w: data after the armored block", errSSHSig)
	}
	r := wireReader{buf: block.Bytes}
	magic := r.raw(len(sshsigMagic))
	version := r.uint32()
	pubBlob := r.str()
	ns := r.str()
	reserved := r.str()
	hashAlg := r.str()
	sigBlob := r.str()
	if r.err != nil {
		return "", fmt.Errorf("%w: %v", errSSHSig, r.err)
	}
	if len(r.buf) != 0 {
		return "", fmt.Errorf("%w: trailing bytes in blob", errSSHSig)
	}
	if string(magic) != sshsigMagic || version != sshsigVersion {
		return "", fmt.Errorf("%w: magic or version", errSSHSig)
	}
	if string(ns) != namespace {
		return "", fmt.Errorf("%w: namespace %q", errSSHSig, ns)
	}
	pub, err := parseEd25519Blob(pubBlob)
	if err != nil {
		return "", err
	}
	var h hash.Hash
	switch string(hashAlg) {
	case "sha512":
		h = sha512.New()
	case "sha256":
		h = sha256.New()
	default:
		return "", fmt.Errorf("%w: hash algorithm %q", errSSHSig, hashAlg)
	}
	sr := wireReader{buf: sigBlob}
	sigType := sr.str()
	sig := sr.str()
	if sr.err != nil || len(sr.buf) != 0 {
		return "", fmt.Errorf("%w: malformed signature field", errSSHSig)
	}
	if string(sigType) != keyTypeEd || len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("%w: signature type %q", errSSHSig, sigType)
	}
	known := false
	for _, k := range keys {
		if len(k) == ed25519.PublicKeySize && subtle.ConstantTimeCompare(k, pub) == 1 {
			known = true
		}
	}
	if !known {
		return "", fmt.Errorf("%w: signed by an unknown key %s", errSSHSig, fingerprint(pubBlob))
	}
	h.Write(message)
	if !ed25519.Verify(pub, signedData(namespace, reserved, hashAlg, h.Sum(nil)), sig) {
		return "", fmt.Errorf("%w: does not verify", errSSHSig)
	}
	return fingerprint(pubBlob), nil
}

// signedData is the byte string an SSHSIG signature covers.
func signedData(namespace string, reserved, hashAlg, digest []byte) []byte {
	var b []byte
	b = append(b, sshsigMagic...)
	b = appendString(b, []byte(namespace))
	b = appendString(b, reserved)
	b = appendString(b, hashAlg)
	return appendString(b, digest)
}

// parseEd25519Blob reads an SSH wire-format public key and accepts only
// ssh-ed25519.
func parseEd25519Blob(blob []byte) (ed25519.PublicKey, error) {
	r := wireReader{buf: blob}
	keyType := r.str()
	key := r.str()
	if r.err != nil || len(r.buf) != 0 {
		return nil, fmt.Errorf("%w: malformed public key", errSSHSig)
	}
	if string(keyType) != keyTypeEd {
		return nil, fmt.Errorf("%w: key type %q is not accepted", errSSHSig, keyType)
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: ed25519 key length %d", errSSHSig, len(key))
	}
	return ed25519.PublicKey(bytes.Clone(key)), nil
}

// parseAuthorizedKeys reads "ssh-ed25519 AAAA… [comment]" lines; blank lines
// and # comments are skipped. It is used for the compiled-in release keys.
func parseAuthorizedKeys(text string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != keyTypeEd {
			return nil, fmt.Errorf("line %d: want %s and a base64 key", n+1, keyTypeEd)
		}
		blob, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", n+1, err)
		}
		key, err := parseEd25519Blob(blob)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n+1, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// fingerprint is the OpenSSH SHA256 fingerprint of a wire-format key.
func fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

// wireReader reads SSH wire primitives; the first error sticks.
type wireReader struct {
	buf []byte
	err error
}

func (r *wireReader) raw(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n > len(r.buf) {
		r.err = errors.New("truncated blob")
		return nil
	}
	v := r.buf[:n]
	r.buf = r.buf[n:]
	return v
}

func (r *wireReader) uint32() uint32 {
	b := r.raw(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (r *wireReader) str() []byte {
	n := r.uint32()
	if r.err != nil {
		return nil
	}
	if uint64(n) > uint64(len(r.buf)) {
		r.err = errors.New("truncated blob")
		return nil
	}
	return r.raw(int(n))
}
