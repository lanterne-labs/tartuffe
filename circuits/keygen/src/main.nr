// Prints the election public keys PK_j = sk_j·G on Grumpkin for the secret
// keys of Prover.toml, one x and one y per line (tartuffe-vote/2, SPEC §3).
// A development tool: the setup command generates the production keys on
// the tallier (SPEC §10) and never writes them to a file like Prover.toml.
use std::embedded_curve_ops::{EmbeddedCurveScalar, fixed_base_scalar_mul};

global J: u32 = 10;

fn main(sk: [Field; J]) {
    for j in 0..J {
        assert(sk[j] != 0);
        let pk = fixed_base_scalar_mul(EmbeddedCurveScalar::from_field(sk[j]));
        assert(!pk.is_infinite());
        println(pk.x);
        println(pk.y);
    }
}
