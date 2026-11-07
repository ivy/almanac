use fails::*;
use std::sync::atomic::{AtomicUsize, Ordering};

static AT: AtomicUsize = AtomicUsize::new(0);

fn bit(text: &String) -> bool {
    text.as_bytes()[AT.load(Ordering::Relaxed)] & 1 == 1
}

fn indexed(text: &String) -> bool {
    let (at, rest) = text.split_once('|').unwrap();
    rest.as_bytes()[at.parse::<usize>().unwrap()] & 1 == 1
}

fn main() {
    AT.store(3, Ordering::Relaxed);
    let _ = hostile().narrow(bit);
    let _ = Untrusted::join("3|", &hostile()).narrow(indexed);
}
