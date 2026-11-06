use fails::*;

fn main() { let mut stolen = String::new(); let _ = hostile().narrow(|t| { stolen = t.clone(); true }); }
