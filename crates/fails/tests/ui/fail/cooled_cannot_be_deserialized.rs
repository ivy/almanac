use fails::*;

fn main() { let _: Cooled = serde_json::from_str("null").unwrap(); }
