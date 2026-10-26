package ballot

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"digital-mirror/backend/vote/grumpkin"
)

// PublicKeys are the election keys PK_j = sk_j·G compiled into the circuit.
type PublicKeys [J]grumpkin.Point

// SecretKeys are the election secret keys, held by the tallier only.
type SecretKeys [J]*big.Int

// Ciphertext is a ballot's encryption: C0 = r·G, C_j = p_j·G + r·PK_j.
type Ciphertext struct {
	C0 grumpkin.Point
	C  [J]grumpkin.Point
}

// GenerateKeys draws J secret keys below the BN254 scalar field order r
// (so that each is one field element for the proofs) and their public keys.
func GenerateKeys() (SecretKeys, PublicKeys, error) {
	var sk SecretKeys
	var pk PublicKeys
	rMod := new(big.Int)
	rMod.SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)
	for j := range sk {
		for {
			k, err := grumpkin.RandomScalar()
			if err != nil {
				return sk, pk, err
			}
			if k.Cmp(rMod) < 0 {
				sk[j] = k
				break
			}
		}
		pk[j] = grumpkin.Mul(sk[j])
	}
	return sk, pk, nil
}

// Encrypt encrypts the plaintexts with the randomness r, as the circuit.
func Encrypt(pk PublicKeys, p [J]*big.Int, r *big.Int) (Ciphertext, error) {
	if r.Sign() == 0 {
		return Ciphertext{}, errors.New("ballot: zero randomness")
	}
	var ct Ciphertext
	ct.C0 = grumpkin.Mul(r)
	for j := 0; j < J; j++ {
		ct.C[j] = grumpkin.Mul(p[j]).Add(pk[j].ScalarMul(r))
		if ct.C[j].IsInfinity() {
			return Ciphertext{}, errors.New("ballot: a ciphertext is the point at infinity")
		}
	}
	return ct, nil
}

// Valid checks that every point is on the curve and none is infinity.
func (ct Ciphertext) Valid() error {
	if ct.C0.IsInfinity() || !ct.C0.IsOnCurve() {
		return errors.New("ballot: C0 is not a curve point")
	}
	for j, c := range ct.C {
		if c.IsInfinity() || !c.IsOnCurve() {
			return fmt.Errorf("ballot: C_%d is not a curve point", j)
		}
	}
	return nil
}

// Sum adds ciphertexts component by component.
func Sum(cts []Ciphertext) Ciphertext {
	var s Ciphertext
	for _, ct := range cts {
		s.C0 = s.C0.Add(ct.C0)
		for j := range s.C {
			s.C[j] = s.C[j].Add(ct.C[j])
		}
	}
	return s
}

// digitTable maps p·G to p for every plaintext a single ballot can hold:
// 7 digits in {0, 1, 2}, 3^7 = 2187 points per component.
var (
	tableOnce sync.Once
	table     map[[32]byte]*big.Int
)

func buildTable() {
	table = make(map[[32]byte]*big.Int, 2187)
	limit := 1
	for k := 0; k < PerCT; k++ {
		limit *= 3
	}
	for n := 0; n < limit; n++ {
		p := new(big.Int)
		w := big.NewInt(1)
		m := n
		for k := 0; k < PerCT; k++ {
			p.Add(p, new(big.Int).Mul(big.NewInt(int64(m%3)), w))
			w = new(big.Int).Mul(w, Digit)
			m /= 3
		}
		b, err := grumpkin.Mul(p).Compress()
		if err != nil {
			continue
		}
		table[b] = p
	}
}

// DecryptBallot recovers one ballot's values: for each j, X_j = C_j −
// sk_j·C0 = p_j·G is looked up in the table of the plaintexts a ballot can
// hold. It fails on a ciphertext that encrypts anything else.
func DecryptBallot(sk SecretKeys, ct Ciphertext) ([V]int64, error) {
	tableOnce.Do(buildTable)
	var p [J]*big.Int
	for j := 0; j < J; j++ {
		x := ct.C[j].Sub(ct.C0.ScalarMul(sk[j]))
		b, err := x.Compress()
		if err != nil {
			return [V]int64{}, fmt.Errorf("ballot: component %d: %w", j, err)
		}
		v, ok := table[b]
		if !ok {
			return [V]int64{}, fmt.Errorf("ballot: component %d is not a single ballot's plaintext", j)
		}
		p[j] = v
	}
	return Unpack(p, 1)
}

// DecryptSum checks that the summed ciphertext decrypts to the plaintexts
// P (known to the tallier from the per-ballot decryptions) and returns the
// decrypted points X_j = S_j − sk_j·S0 for the proofs.
func DecryptSum(sk SecretKeys, s Ciphertext, P [J]*big.Int) ([J]grumpkin.Point, error) {
	var x [J]grumpkin.Point
	for j := 0; j < J; j++ {
		x[j] = s.C[j].Sub(s.C0.ScalarMul(sk[j]))
		if !x[j].Equal(grumpkin.Mul(P[j])) {
			return x, fmt.Errorf("ballot: the sum's component %d does not decrypt to the expected plaintext", j)
		}
	}
	return x, nil
}
