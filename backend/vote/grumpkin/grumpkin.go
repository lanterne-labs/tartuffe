// Package grumpkin is the Grumpkin curve, y² = x³ − 17 over the BN254
// scalar field, with scalars in the BN254 base field: the embedded curve of
// Noir, the E of docs/vote/SPEC.md §3. Affine points, the point at infinity
// as (0, 0), the generator (1, √−16) of the Noir standard library, and the
// compressed encoding of SPEC §6.4 (x, with the parity of y in the top bit).
package grumpkin

import (
	"errors"
	"math/big"

	"github.com/consensys/gnark-crypto/ecc/bn254/fp"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
)

// Point is an affine point; X = Y = 0 is the point at infinity.
type Point struct {
	X, Y fr.Element
}

// Order is the order of the group, the BN254 base field modulus q.
var Order = fp.Modulus()

var (
	seventeen fr.Element
	gen       Point
)

func init() {
	seventeen.SetUint64(17)
	gen.X.SetOne()
	gen.Y.SetString("17631683881184975370165255887551781615748388533673675138860")
	if !gen.IsOnCurve() {
		panic("grumpkin: the generator is not on the curve")
	}
}

// Generator returns G.
func Generator() Point { return gen }

// Infinity returns the point at infinity.
func Infinity() Point { return Point{} }

// IsInfinity tells whether p is the point at infinity.
func (p Point) IsInfinity() bool { return p.X.IsZero() && p.Y.IsZero() }

// IsOnCurve tells whether p satisfies y² = x³ − 17 (infinity counts).
func (p Point) IsOnCurve() bool {
	if p.IsInfinity() {
		return true
	}
	var l, r fr.Element
	l.Square(&p.Y)
	r.Square(&p.X)
	r.Mul(&r, &p.X)
	r.Sub(&r, &seventeen)
	return l.Equal(&r)
}

// Equal compares two points.
func (p Point) Equal(q Point) bool { return p.X.Equal(&q.X) && p.Y.Equal(&q.Y) }

// Neg returns −p.
func (p Point) Neg() Point {
	if p.IsInfinity() {
		return p
	}
	var y fr.Element
	y.Neg(&p.Y)
	return Point{X: p.X, Y: y}
}

// Double returns 2p.
func (p Point) Double() Point {
	if p.IsInfinity() || p.Y.IsZero() {
		return Infinity()
	}
	// λ = 3x² / 2y
	var num, den, l, x3, y3 fr.Element
	three := fr.NewElement(3)
	num.Square(&p.X)
	num.Mul(&num, &three)
	den.Double(&p.Y)
	den.Inverse(&den)
	l.Mul(&num, &den)
	x3.Square(&l)
	x3.Sub(&x3, &p.X)
	x3.Sub(&x3, &p.X)
	y3.Sub(&p.X, &x3)
	y3.Mul(&y3, &l)
	y3.Sub(&y3, &p.Y)
	return Point{X: x3, Y: y3}
}

// Add returns p + q, with every special case handled.
func (p Point) Add(q Point) Point {
	if p.IsInfinity() {
		return q
	}
	if q.IsInfinity() {
		return p
	}
	if p.X.Equal(&q.X) {
		if p.Y.Equal(&q.Y) {
			return p.Double()
		}
		return Infinity()
	}
	var num, den, l, x3, y3 fr.Element
	num.Sub(&q.Y, &p.Y)
	den.Sub(&q.X, &p.X)
	den.Inverse(&den)
	l.Mul(&num, &den)
	x3.Square(&l)
	x3.Sub(&x3, &p.X)
	x3.Sub(&x3, &q.X)
	y3.Sub(&p.X, &x3)
	y3.Mul(&y3, &l)
	y3.Sub(&y3, &p.Y)
	return Point{X: x3, Y: y3}
}

// Sub returns p − q.
func (p Point) Sub(q Point) Point { return p.Add(q.Neg()) }

// ScalarMul returns k·p for a non-negative k (reduced modulo the order).
func (p Point) ScalarMul(k *big.Int) Point {
	k = new(big.Int).Mod(k, Order)
	r := Infinity()
	for i := k.BitLen() - 1; i >= 0; i-- {
		r = r.Double()
		if k.Bit(i) == 1 {
			r = r.Add(p)
		}
	}
	return r
}

// Mul returns k·G.
func Mul(k *big.Int) Point { return gen.ScalarMul(k) }

// Compress encodes p as 32 bytes: x in big-endian with the parity of y in
// the top bit. The point at infinity is not encodable.
func (p Point) Compress() ([32]byte, error) {
	var out [32]byte
	if p.IsInfinity() {
		return out, errors.New("grumpkin: the point at infinity has no encoding")
	}
	if !p.IsOnCurve() {
		return out, errors.New("grumpkin: not on the curve")
	}
	out = p.X.Bytes()
	if out[0]&0x80 != 0 {
		return out, errors.New("grumpkin: x has the top bit set")
	}
	var y big.Int
	p.Y.BigInt(&y)
	if y.Bit(0) == 1 {
		out[0] |= 0x80
	}
	return out, nil
}

// Decompress decodes the encoding of Compress, rejecting anything that
// is not canonical: x not below the field, no square root, infinity.
func Decompress(b [32]byte) (Point, error) {
	parity := b[0]&0x80 != 0
	b[0] &^= 0x80
	var x fr.Element
	if err := x.SetBytesCanonical(b[:]); err != nil {
		return Point{}, errors.New("grumpkin: x is not a canonical field element")
	}
	var rhs fr.Element
	rhs.Square(&x)
	rhs.Mul(&rhs, &x)
	rhs.Sub(&rhs, &seventeen)
	var y fr.Element
	if y.Sqrt(&rhs) == nil {
		return Point{}, errors.New("grumpkin: not on the curve")
	}
	var yi big.Int
	y.BigInt(&yi)
	if (yi.Bit(0) == 1) != parity {
		y.Neg(&y)
	}
	p := Point{X: x, Y: y}
	if p.IsInfinity() {
		return Point{}, errors.New("grumpkin: the point at infinity")
	}
	return p, nil
}

// RandomScalar draws a scalar in [1, Order).
func RandomScalar() (*big.Int, error) {
	var e fp.Element
	if _, err := e.SetRandom(); err != nil {
		return nil, err
	}
	k := e.BigInt(new(big.Int))
	if k.Sign() == 0 {
		k.SetUint64(1)
	}
	return k, nil
}
