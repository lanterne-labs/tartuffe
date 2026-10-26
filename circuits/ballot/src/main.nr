// The ballot statement of tartuffe-vote/2 (docs/vote/SPEC.md §6.2): one
// UltraHonk proof that a ballot comes from a registered key, names that
// key's pseudonym, follows the ranking rules of its setup, and encrypts
// the ranking's values for the homomorphic tally.
//
// Public inputs, in this order: event, setup, mask, root, nullifier, seq,
// then the ciphertexts as the return value: C0.x, C0.y, C_0.x, C_0.y, …,
// C_9.x, C_9.y (22 fields).
//
// Private inputs: the voter secret s, the Merkle path of the leaf
// H(T_ID, event, s) under root (D siblings and the leaf index), the ranking
// (len, slots[0..3]) and the encryption randomness r.
//
// The election public keys PK are compiled in (consts.nr, one circuit per
// key set); the verifier takes mask and the verification key from the setup
// the ballot names, never from the ballot.
use poseidon::poseidon2::Poseidon2;
use std::embedded_curve_ops::{
    EmbeddedCurvePoint, EmbeddedCurveScalar, fixed_base_scalar_mul, multi_scalar_mul,
};

mod consts;
use consts::{PK, T_ID, T_NODE, T_NULL};

/// Option slots: candidates, then blank, then reserved (SPEC §6.1).
pub global N: u32 = 10;
pub global BLANK: u32 = 7;
/// Values per ballot: voter, first[N], ranked[N], margin[N(N-1)/2].
pub global V: u32 = 66;
/// Values per ciphertext and ciphertexts per ballot.
pub global PER_CT: u32 = 7;
pub global J: u32 = 10;
/// Bits per packed digit: 2^36.
global DIGIT: Field = 68719476736;
/// Tree depth: a million leaves.
pub global D: u32 = 20;
global LEAVES: u32 = 1048576;

/// H(tag, a, b): Poseidon2 over three field elements (SPEC §3).
pub fn h3(tag: Field, a: Field, b: Field) -> Field {
    Poseidon2::hash([tag, a, b], 3)
}

/// The root of the tree that holds `leaf` at `index`, from its siblings;
/// bit i of the index (least significant first) says whether the node at
/// level i is the right child.
pub fn merkle_root(leaf: Field, path: [Field; D], index: u32) -> Field {
    assert(index < LEAVES);
    let bits: [bool; D] = (index as Field).to_le_bits();
    let mut cur = leaf;
    for i in 0..D {
        let (l, r) = if bits[i] { (path[i], cur) } else { (cur, path[i]) };
        cur = h3(T_NODE, l, r);
    }
    cur
}

/// The packed digits of a ranking: value + 1 for each of the V values of
/// SPEC §6.1, in order (voter, first[c], ranked[c], margin[i<j]).
pub fn digits(len: u32, slots: [u32; 3], mask: u32) -> [Field; V] {
    assert(len <= 3);
    // pos[c]: the rank of option c, 3 when unranked.
    let mut pos: [u32; N] = [3; N];
    for k in 0..3 {
        let o = slots[k];
        if k < len {
            assert(o < N);
            assert(((mask >> o) & 1) == 1);
            for j in 0..3 {
                if j < k {
                    assert(slots[j] != o);
                }
            }
            if o == BLANK {
                assert(k == len - 1);
            }
            for c in 0..N {
                if o == c {
                    pos[c] = k;
                }
            }
        } else {
            assert(o == 0);
        }
    }
    let mut d: [Field; V] = [1; V];
    d[0] = if len >= 1 { 2 } else { 1 };
    for c in 0..N {
        d[1 + c] = if (len >= 1) & (slots[0] == c) { 2 } else { 1 };
        d[1 + N + c] = if pos[c] < 3 { 2 } else { 1 };
    }
    let mut v = 1 + 2 * N;
    for i in 0..N {
        for j in 0..N {
            if i < j {
                d[v] = if pos[i] < pos[j] {
                    2
                } else if pos[j] < pos[i] {
                    0
                } else {
                    1
                };
                v += 1;
            }
        }
    }
    d
}

/// The J plaintexts: 7 digits each, base 2^36, the first value in the
/// lowest digit; the unused digits of the last plaintext hold 1 (value 0).
pub fn pack(d: [Field; V]) -> [Field; J] {
    let mut p: [Field; J] = [0; J];
    for j in 0..J {
        let mut acc: Field = 0;
        let mut w: Field = 1;
        for k in 0..PER_CT {
            let idx = PER_CT * j + k;
            let dig = if idx < V { d[idx] } else { 1 };
            acc += dig * w;
            w *= DIGIT;
        }
        p[j] = acc;
    }
    p
}

fn main(
    event: pub Field,
    setup: pub Field,
    mask: pub u32,
    root: pub Field,
    nullifier: pub Field,
    seq: pub u32,
    s: Field,
    path: [Field; D],
    index: u32,
    slots: [u32; 3],
    len: u32,
    r: Field,
) -> pub [Field; 22] {
    // 1. Membership. 2. Pseudonym. 3. seq < 2^32 by its type. 7. setup and
    // seq are bound by being public inputs of this proof.
    let id = h3(T_ID, event, s);
    assert(merkle_root(id, path, index) == root);
    assert(nullifier == h3(T_NULL, event, s));
    let _bound = (setup, seq);

    // 4, 5. The ranking and its packed values.
    let p = pack(digits(len, slots, mask));

    // 6. Multi-recipient exponential ElGamal: C0 = r·G, C_j = p_j·G + r·PK_j.
    assert(r != 0);
    let rs = EmbeddedCurveScalar::from_field(r);
    let g = EmbeddedCurvePoint::generator();
    let c0 = fixed_base_scalar_mul(rs);
    assert(!c0.is_infinite());
    let mut out: [Field; 22] = [0; 22];
    out[0] = c0.x;
    out[1] = c0.y;
    for j in 0..J {
        let c = multi_scalar_mul([PK[j], g], [rs, EmbeddedCurveScalar::from_field(p[j])]);
        assert(!c.is_infinite());
        out[2 + 2 * j] = c.x;
        out[3 + 2 * j] = c.y;
    }
    out
}

mod tests {
    use super::{BLANK, D, digits, h3, merkle_root, N, pack, V};
    use super::consts::T_NODE;

    /// A tree holding `leaves` at positions 0.., and the path of position 1.
    fn small_tree(leaves: [Field; 3]) -> (Field, [Field; D]) {
        let mut zero: [Field; D] = [0; D];
        for i in 1..D {
            zero[i] = h3(T_NODE, zero[i - 1], zero[i - 1]);
        }
        let mut path: [Field; D] = zero;
        path[0] = leaves[0];
        path[1] = h3(T_NODE, leaves[2], 0);
        let root = merkle_root(leaves[1], path, 1);
        (root, path)
    }

    /// Prints the vector the JavaScript side must reproduce (scripts/vectors.mjs).
    #[test]
    fn poseidon2_vector() {
        let h = h3(1, 2, 3);
        println(h);
        assert(h != 0);
    }

    #[test]
    fn membership_holds_for_the_logged_leaf() {
        let (root, path) = small_tree([11, 22, 33]);
        assert(merkle_root(22, path, 1) == root);
    }

    #[test(should_fail)]
    fn membership_fails_for_another_leaf() {
        let (root, path) = small_tree([11, 22, 33]);
        assert(merkle_root(44, path, 1) == root);
    }

    #[test(should_fail)]
    fn membership_fails_at_another_index() {
        let (root, path) = small_tree([11, 22, 33]);
        assert(merkle_root(22, path, 0) == root);
    }

    /// Candidates 0..6 and blank allowed, the reserved slots not.
    global MASK: u32 = 255;

    #[test]
    fn full_ranking_values() {
        // 4 > 2 > blank: first[4], ranked {2, 4, blank}, 4 before everyone,
        // 2 before everyone but 4, blank before the unranked.
        let d = digits(3, [4, 2, BLANK], MASK);
        assert(d[0] == 2);
        for c in 0..N {
            assert(d[1 + c] == if c == 4 { 2 } else { 1 });
            assert(d[1 + N + c] == if (c == 4) | (c == 2) | (c == BLANK) { 2 } else { 1 });
        }
        // margin[2 < 4] = -1 (4 before 2): digit 0.
        let mut v = 1 + 2 * N;
        for i in 0..N {
            for j in 0..N {
                if i < j {
                    let ranked_i = (i == 4) | (i == 2) | (i == BLANK);
                    let ranked_j = (j == 4) | (j == 2) | (j == BLANK);
                    let expect = if (i == 2) & (j == 4) {
                        0 // 4 is ranked before 2
                    } else if ranked_i {
                        2 // i ranked: before an unranked j, or before blank
                    } else if ranked_j {
                        0 // i unranked, j ranked
                    } else {
                        1 // both unranked
                    };
                    assert(d[v] == expect);
                    v += 1;
                }
            }
        }
    }

    #[test]
    fn withdrawal_is_all_zero_values() {
        let d = digits(0, [0, 0, 0], MASK);
        for i in 0..V {
            assert(d[i] == 1);
        }
    }

    #[test]
    fn blank_alone() {
        let d = digits(1, [BLANK, 0, 0], MASK);
        assert(d[0] == 2);
        assert(d[1 + BLANK] == 2);
        assert(d[1 + N + BLANK] == 2);
    }

    #[test(should_fail)]
    fn blank_must_be_last() {
        let _ = digits(2, [BLANK, 1, 0], MASK);
    }

    #[test(should_fail)]
    fn options_are_distinct() {
        let _ = digits(2, [3, 3, 0], MASK);
    }

    #[test(should_fail)]
    fn reserved_slot_is_refused() {
        let _ = digits(1, [8, 0, 0], MASK);
    }

    #[test(should_fail)]
    fn unused_slots_are_zero() {
        let _ = digits(1, [3, 5, 0], MASK);
    }

    #[test(should_fail)]
    fn at_most_three() {
        let _ = digits(4, [0, 1, 2], MASK);
    }

    #[test]
    fn packing_puts_the_first_value_lowest() {
        let mut d: [Field; V] = [1; V];
        d[0] = 2;
        d[7] = 0;
        let p = pack(d);
        // p_0 = 2 + 1·2^36 + … + 1·2^(36·6)
        let mut expect: Field = 0;
        let mut w: Field = 1;
        for k in 0..7 {
            expect += (if k == 0 { 2 } else { 1 }) * w;
            w *= 68719476736;
        }
        assert(p[0] == expect);
        // p_1's lowest digit is d[7] = 0.
        let mut e1: Field = 0;
        let mut w1: Field = 1;
        for k in 0..7 {
            e1 += (if k == 0 { 0 } else { 1 }) * w1;
            w1 *= 68719476736;
        }
        assert(p[1] == e1);
        // The padding digits of p_9 are 1.
        let mut e9: Field = 0;
        let mut w9: Field = 1;
        for _ in 0..7 {
            e9 += w9;
            w9 *= 68719476736;
        }
        assert(p[9] == e9);
    }
}
