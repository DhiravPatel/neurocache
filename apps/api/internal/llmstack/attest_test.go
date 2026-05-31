package llmstack

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestAttestLogAppendsAndChains(t *testing.T) {
	a := NewAttestation()
	r1, err := a.Log("audit", `{"actor":"x","amount":10}`)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Seq != 0 {
		t.Fatalf("first seq = %d", r1.Seq)
	}
	r2, _ := a.Log("audit", `{"actor":"y","amount":20}`)
	if r2.PrevHash != r1.LeafHash {
		t.Fatalf("chain broken: %s != %s", r2.PrevHash, r1.LeafHash)
	}
}

func TestAttestCanonicalizationDeterministic(t *testing.T) {
	a := NewAttestation()
	r1, _ := a.Log("l1", `{"b":2,"a":1}`)
	b := NewAttestation()
	r2, _ := b.Log("l1", `{"a":1,"b":2}`)
	if r1.LeafHash != r2.LeafHash {
		t.Fatalf("key order should not affect hash: %s != %s", r1.LeafHash, r2.LeafHash)
	}
}

func TestAttestInvalidJSONRejected(t *testing.T) {
	a := NewAttestation()
	if _, err := a.Log("l", "{not json"); err == nil {
		t.Fatal("invalid JSON should fail")
	}
}

func TestAttestRootChangesWithEachLog(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{"a":1}`)
	r1, _ := a.Root("l")
	a.Log("l", `{"a":2}`)
	r2, _ := a.Root("l")
	if r1.MerkleRoot == r2.MerkleRoot {
		t.Fatal("root should change when leaves added")
	}
}

func TestAttestProveAndVerifyOffline(t *testing.T) {
	a := NewAttestation()
	for i := 0; i < 7; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	proof, ok := a.Prove("l", 3)
	if !ok {
		t.Fatal("proof missing")
	}
	// Now verify offline (stateless function)
	valid, err := a.Verify(proof.Root, proof.Canon, proof.Path, proof.Indices)
	if err != nil || !valid {
		t.Fatalf("offline verify failed: valid=%v err=%v", valid, err)
	}
}

func TestAttestVerifyRejectsTamperedLeaf(t *testing.T) {
	a := NewAttestation()
	for i := 0; i < 7; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	proof, _ := a.Prove("l", 3)
	tampered := `{"i":999}` // pretend the attacker edited the leaf
	valid, _ := a.Verify(proof.Root, tampered, proof.Path, proof.Indices)
	if valid {
		t.Fatal("tampered leaf should fail verify")
	}
}

func TestAttestVerifyRejectsTamperedPath(t *testing.T) {
	a := NewAttestation()
	for i := 0; i < 7; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	proof, _ := a.Prove("l", 3)
	// Replace one sibling with garbage
	bad := append([]string{}, proof.Path...)
	bad[0] = "deadbeef" + bad[0][8:]
	valid, _ := a.Verify(proof.Root, proof.Canon, bad, proof.Indices)
	if valid {
		t.Fatal("tampered path should fail verify")
	}
}

func TestAttestVerifyEveryIndex(t *testing.T) {
	a := NewAttestation()
	for i := 0; i < 16; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	for i := int64(0); i < 16; i++ {
		proof, _ := a.Prove("l", i)
		valid, _ := a.Verify(proof.Root, proof.Canon, proof.Path, proof.Indices)
		if !valid {
			t.Fatalf("leaf %d failed verify", i)
		}
	}
}

func TestAttestSingleLeafProof(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{"a":1}`)
	proof, _ := a.Prove("l", 0)
	valid, _ := a.Verify(proof.Root, proof.Canon, proof.Path, proof.Indices)
	if !valid {
		t.Fatal("single-leaf proof should verify")
	}
}

func TestAttestProveOutOfRange(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{}`)
	if _, ok := a.Prove("l", 99); ok {
		t.Fatal("out-of-range seq should fail")
	}
}

func TestAttestSealAndSign(t *testing.T) {
	a := NewAttestation()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	a.Log("l", `{"a":1}`)
	if err := a.Seal("l", pub); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Sign("l", 0, priv); err != nil {
		t.Fatal(err)
	}
	valid, reason, _ := a.VerifySig("l", 0)
	if !valid {
		t.Fatalf("verify_sig: %s", reason)
	}
}

func TestAttestVerifySigRejectsWrongKey(t *testing.T) {
	a := NewAttestation()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, wrongPriv, _ := ed25519.GenerateKey(rand.Reader) // mismatched
	a.Log("l", `{"a":1}`)
	a.Seal("l", pub)
	a.Sign("l", 0, wrongPriv)
	valid, _, _ := a.VerifySig("l", 0)
	if valid {
		t.Fatal("wrong-key signature should fail")
	}
}

func TestAttestVerifySigNoKey(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{}`)
	valid, _, _ := a.VerifySig("l", 0)
	if valid {
		t.Fatal("no key should not be valid")
	}
}

func TestAttestSealRejectsBadKey(t *testing.T) {
	a := NewAttestation()
	if err := a.Seal("l", []byte{1, 2, 3}); err == nil {
		t.Fatal("short key should fail")
	}
}

func TestAttestReceiptBundlesProof(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{"a":1}`)
	r, ok := a.Receipt("l", 0, nil, "")
	if !ok {
		t.Fatal("receipt missing")
	}
	if r.Proof.LeafHash == "" || r.Proof.Root == "" {
		t.Fatalf("receipt empty: %+v", r)
	}
}

func TestAttestReceiptWithProvenance(t *testing.T) {
	a := NewAttestation()
	p := NewProvenance()
	p.Begin("ans-1", nil)
	p.Node("ans-1", "n", "k", "l", nil, []string{"doc:44"})
	a.Log("audit", `{"ans":"ans-1"}`)
	r, _ := a.Receipt("audit", 0, p, "ans-1")
	if r.Provenance == nil {
		t.Fatal("provenance missing from receipt")
	}
	if r.Provenance.AnswerID != "ans-1" {
		t.Fatalf("wrong answer: %s", r.Provenance.AnswerID)
	}
}

func TestAttestScanStreaming(t *testing.T) {
	a := NewAttestation()
	for i := 0; i < 50; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	rows, _ := a.Scan("l", 10, 5)
	if len(rows) != 5 || rows[0].Seq != 10 {
		t.Fatalf("scan = %+v", rows)
	}
}

func TestAttestHead(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{}`)
	a.Log("l", `{}`)
	h, ok := a.Head("l")
	if !ok || h.Seq != 2 {
		t.Fatalf("head: %+v", h)
	}
	if h.HeadHash == "" {
		t.Fatal("head hash empty")
	}
}

func TestAttestStats(t *testing.T) {
	a := NewAttestation()
	a.Log("l", `{}`)
	a.Root("l")
	a.Prove("l", 0)
	s := a.Stats()
	if s.TotalLogs != 1 || s.TotalRoots != 1 || s.TotalProves != 1 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestAttestForget(t *testing.T) {
	a := NewAttestation()
	a.Log("a", `{}`)
	a.Log("b", `{}`)
	if a.Forget("a") != 1 {
		t.Fatal("forget a")
	}
	if a.Forget("ALL") != 1 {
		t.Fatal("ALL")
	}
}

func TestAttestRejectsBadInput(t *testing.T) {
	a := NewAttestation()
	if _, err := a.Log("", `{}`); err == nil {
		t.Fatal("empty log should fail")
	}
	if _, err := a.Log("l", ""); err == nil {
		t.Fatal("empty payload should fail")
	}
}

func TestAttestVerifyBadHex(t *testing.T) {
	a := NewAttestation()
	if _, err := a.Verify("zzznotahex", `{}`, nil, nil); err == nil {
		t.Fatal("bad root hex should fail")
	}
}

func TestAttestChainBreakDetectableViaProof(t *testing.T) {
	// Demonstrates the property: if an attacker silently swaps any leaf
	// in the in-memory log, the root changes and any saved old root no
	// longer verifies.
	a := NewAttestation()
	for i := 0; i < 5; i++ {
		a.Log("l", `{"i":`+itoaBench(i)+`}`)
	}
	rootBefore, _ := a.Root("l")
	proof, _ := a.Prove("l", 2)
	// Confirm the old root verifies the proof
	valid, _ := a.Verify(rootBefore.MerkleRoot, proof.Canon, proof.Path, proof.Indices)
	if !valid {
		t.Fatal("proof should verify against pre-tamper root")
	}
	// Now simulate tamper: replace leaf 2's canon and rebuild.
	// (We mutate the log directly to simulate an attacker bypassing LOG)
	a.mu.RLock()
	l := a.logs["l"]
	a.mu.RUnlock()
	l.mu.Lock()
	l.leaves[2].Canon = []byte(`{"i":"hacked"}`)
	l.mu.Unlock()
	rootAfter, _ := a.Root("l")
	if rootAfter.MerkleRoot == rootBefore.MerkleRoot {
		t.Fatal("root must change after tamper")
	}
}

func TestAttestVerifyReproducibility(t *testing.T) {
	// Demonstrates the cross-language property: the same canon JSON
	// produces the same hash regardless of original whitespace/order.
	a := NewAttestation()
	r, _ := a.Log("l", `{"b":2,"a":1,"c":3}`)
	h, _ := hex.DecodeString(r.LeafHash)
	if len(h) != 32 {
		t.Fatalf("hash length = %d", len(h))
	}
}

func BenchmarkAttestLog(b *testing.B) {
	a := NewAttestation()
	for i := 0; i < b.N; i++ {
		a.Log("bench", `{"i":`+itoaBench(i)+`,"actor":"x"}`)
	}
}

func BenchmarkAttestProve(b *testing.B) {
	a := NewAttestation()
	for i := 0; i < 10000; i++ {
		a.Log("bench", `{"i":`+itoaBench(i)+`}`)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Prove("bench", int64(i%10000))
	}
}
