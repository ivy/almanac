use fails::*;

fn forge(patch: Patch, base: Sha) -> Checked { Checked { patch, base } }

fn main() { let _ = forge; }
