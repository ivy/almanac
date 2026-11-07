use fails::*;

fn main() {
    let hostile = String::from("x");
    let _ = title!("{hostile}");
}
