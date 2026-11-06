use fails::*;

fn main() { let _: u64 = hostile().narrow(|t| t.len() as u64); }
