use fails::*;

fn again(proof: &Cooled) -> Cooled { proof.clone() }

fn main() { let _ = again; }
