use fails::*;

fn f(p: &Payload<'_>) { let _: String = p.text("/title").unwrap(); }
fn main() {}
