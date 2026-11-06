use fails::*;

struct Sneaky(String);
impl almanac::__private::Sealed for Sneaky {}
impl Clean for Sneaky {}
fn main() {}
