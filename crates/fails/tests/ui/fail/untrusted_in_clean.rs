use fails::*;

clean! { struct Sneaky { note: Untrusted<String> } }
fn main() {}
