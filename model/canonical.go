package model

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Canonical is w as JSON with every object's keys sorted and no insignificant whitespace, so
// the same model always has the same bytes. It is what a store holds and what a run records as
// the version it ran.
func Canonical(w Workflow) ([]byte, error) {
	plain, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// Digest names a version of w: "sha256:" and the hex digest of its canonical JSON.
func Digest(w Workflow) (string, error) {
	canonical, err := Canonical(w)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
