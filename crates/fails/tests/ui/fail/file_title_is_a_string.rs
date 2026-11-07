use fails::*;

fn main() {
    let _ = Effect::File {
        title: String::from("x"),
        target: "a/b".parse().unwrap(),
        body: Untrusted::new(String::new()),
    };
}
