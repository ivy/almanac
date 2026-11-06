use fails::*;

fn f(p: &Payload<'_>) { let _: String = p.field("/title").unwrap(); }
fn main() {}
