use fails::*;

fn main() { let _: Slug = hostile().narrow(|t| t.parse().unwrap()); }
