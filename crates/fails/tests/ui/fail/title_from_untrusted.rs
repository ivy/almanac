use fails::*;

fn main() {
    let sample = Untrusted::new("x".to_string());
    let _ = title!("{}", sample);
}
