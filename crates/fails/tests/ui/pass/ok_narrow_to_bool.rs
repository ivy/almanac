use fails::*;

fn main() { let _: bool = hostile().narrow(|t| t.len() > 3); }
