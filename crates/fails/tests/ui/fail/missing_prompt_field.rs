use fails::*;

#[prompt(text = "{{ a }} {{ b }}")]
struct P { a: u64, b: u64 }
fn main() { let _ = P { a: 1 }; }
