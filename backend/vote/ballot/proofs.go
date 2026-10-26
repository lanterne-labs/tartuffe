package ballot

import (
	"errors"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"

	"digital-mirror/backend/vote/grumpkin"
	"digital-mirror/backend/vote/poseidon2"
)

// DLEQ is a Chaum-Pedersen proof that log_G PK_j = log_{S0}(S_j − P_j·G):
// the tallier decrypted the sum's component j to P_j with the key of PK_j
// (SPEC §9.1).
type DLEQ struct {
	A, B grumpkin.Point
	Z    *big.Int
}

func pt(p grumpkin.Point) []fr.Element { return []fr.Element{p.X, p.Y} }

func scalar(x *big.Int) fr.Element {
	var e fr.Element
	e.SetBigInt(x)
	return e
}

func uint64El(x uint64) fr.Element {
	var e fr.Element
	e.SetUint64(x)
	return e
}

// dleqChallenge is e = H(T_DLEQ, event, upto, j, G, PK_j, S0, S_j, P_j, A, B).
func dleqChallenge(event fr.Element, upto uint64, j int, pk, s0, sj grumpkin.Point, pj *big.Int, a, b grumpkin.Point) *big.Int {
	in := []fr.Element{TagDLEQ, event, uint64El(upto), uint64El(uint64(j))}
	in = append(in, pt(grumpkin.Generator())...)
	in = append(in, pt(pk)...)
	in = append(in, pt(s0)...)
	in = append(in, pt(sj)...)
	in = append(in, scalar(pj))
	in = append(in, pt(a)...)
	in = append(in, pt(b)...)
	e := poseidon2.Hash(in...)
	return e.BigInt(new(big.Int))
}

// ProveDLEQ proves the decryption of the sum's component j. The nonce is
// derived: k = H(T_NONCE, sk_j, event, upto, j, S0, S_j, P_j).
func ProveDLEQ(skj *big.Int, pk grumpkin.Point, event fr.Element, upto uint64, j int, s0, sj grumpkin.Point, pj *big.Int) DLEQ {
	in := []fr.Element{TagNonce, scalar(skj), event, uint64El(upto), uint64El(uint64(j))}
	in = append(in, pt(s0)...)
	in = append(in, pt(sj)...)
	in = append(in, scalar(pj))
	kEl := poseidon2.Hash(in...)
	k := kEl.BigInt(new(big.Int))
	if k.Sign() == 0 {
		k.SetUint64(1)
	}
	a := grumpkin.Mul(k)
	b := s0.ScalarMul(k)
	e := dleqChallenge(event, upto, j, pk, s0, sj, pj, a, b)
	z := new(big.Int).Mul(e, skj)
	z.Add(z, k)
	z.Mod(z, grumpkin.Order)
	return DLEQ{A: a, B: b, Z: z}
}

// VerifyDLEQ checks a proof: z·G = A + e·PK_j and z·S0 = B + e·(S_j − P_j·G).
func VerifyDLEQ(p DLEQ, pk grumpkin.Point, event fr.Element, upto uint64, j int, s0, sj grumpkin.Point, pj *big.Int) error {
	if p.Z == nil || p.Z.Sign() < 0 || p.Z.Cmp(grumpkin.Order) >= 0 {
		return errors.New("ballot: dleq response out of range")
	}
	if !p.A.IsOnCurve() || !p.B.IsOnCurve() || !pk.IsOnCurve() || !s0.IsOnCurve() || !sj.IsOnCurve() {
		return errors.New("ballot: dleq point off the curve")
	}
	e := dleqChallenge(event, upto, j, pk, s0, sj, pj, p.A, p.B)
	if !grumpkin.Mul(p.Z).Equal(p.A.Add(pk.ScalarMul(e))) {
		return errors.New("ballot: dleq fails on the key")
	}
	x := sj.Sub(grumpkin.Mul(pj))
	if !s0.ScalarMul(p.Z).Equal(p.B.Add(x.ScalarMul(e))) {
		return errors.New("ballot: dleq fails on the decryption")
	}
	return nil
}

// PoK is a Schnorr proof of knowledge of sk_j behind PK_j (the setup's
// pk_pok, SPEC §11), with challenge H(T_POK, event, j, PK_j, A).
type PoK struct {
	A grumpkin.Point
	Z *big.Int
}

func pokChallenge(event fr.Element, j int, pk, a grumpkin.Point) *big.Int {
	in := []fr.Element{TagPoK, event, uint64El(uint64(j))}
	in = append(in, pt(pk)...)
	in = append(in, pt(a)...)
	e := poseidon2.Hash(in...)
	return e.BigInt(new(big.Int))
}

// ProvePoK proves knowledge of skj with a fresh random nonce.
func ProvePoK(skj *big.Int, event fr.Element, j int) (PoK, error) {
	k, err := grumpkin.RandomScalar()
	if err != nil {
		return PoK{}, err
	}
	pk := grumpkin.Mul(skj)
	a := grumpkin.Mul(k)
	e := pokChallenge(event, j, pk, a)
	z := new(big.Int).Mul(e, skj)
	z.Add(z, k)
	z.Mod(z, grumpkin.Order)
	return PoK{A: a, Z: z}, nil
}

// VerifyPoK checks z·G = A + e·PK_j, and that PK_j is a proper key.
func VerifyPoK(p PoK, pk grumpkin.Point, event fr.Element, j int) error {
	if pk.IsInfinity() || !pk.IsOnCurve() || pk.Equal(grumpkin.Generator()) || pk.Equal(grumpkin.Generator().Neg()) {
		return errors.New("ballot: the public key is infinity, G or −G")
	}
	if p.Z == nil || p.Z.Sign() < 0 || p.Z.Cmp(grumpkin.Order) >= 0 || !p.A.IsOnCurve() {
		return errors.New("ballot: pok out of range")
	}
	e := pokChallenge(event, j, pk, p.A)
	if !grumpkin.Mul(p.Z).Equal(p.A.Add(pk.ScalarMul(e))) {
		return errors.New("ballot: pok fails")
	}
	return nil
}
