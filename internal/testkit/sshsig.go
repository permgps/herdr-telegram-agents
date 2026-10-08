package testkit

import (
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
)

// SignSSH produces an armored SSHSIG (sha512) over msg, as
// `ssh-keygen -Y sign -n namespace` would. Tests only.
func SignSSH(priv ed25519.PrivateKey, namespace string, msg []byte) []byte {
	pub := sshKeyBlob(priv.Public().(ed25519.PublicKey))
	digest := sha512.Sum512(msg)
	var signed []byte
	signed = append(signed, "SSHSIG"...)
	signed = sshString(signed, []byte(namespace))
	signed = sshString(signed, nil)
	signed = sshString(signed, []byte("sha512"))
	signed = sshString(signed, digest[:])
	var sig []byte
	sig = sshString(sig, []byte("ssh-ed25519"))
	sig = sshString(sig, ed25519.Sign(priv, signed))
	var blob []byte
	blob = append(blob, "SSHSIG"...)
	blob = binary.BigEndian.AppendUint32(blob, 1)
	blob = sshString(blob, pub)
	blob = sshString(blob, []byte(namespace))
	blob = sshString(blob, nil)
	blob = sshString(blob, []byte("sha512"))
	blob = sshString(blob, sig)
	return pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: blob})
}

// AuthorizedKey renders pub as an "ssh-ed25519 AAAA…" line without comment.
func AuthorizedKey(pub ed25519.PublicKey) string {
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(sshKeyBlob(pub))
}

func sshKeyBlob(pub ed25519.PublicKey) []byte {
	var b []byte
	b = sshString(b, []byte("ssh-ed25519"))
	return sshString(b, pub)
}

func sshString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}
