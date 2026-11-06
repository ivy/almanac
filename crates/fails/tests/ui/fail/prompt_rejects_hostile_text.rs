use fails::*;

#[prompt(text = "Review {{ title }}")]
struct P { title: Untrusted<String> }
fn main() {}
