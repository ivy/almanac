use fails::*;

fn main() { let _: Checked = serde_json::from_str("null").unwrap(); }
