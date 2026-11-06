use almanac::prelude::*;

pub mod build_failures;
// almanac:modules

pub fn all() -> Vec<Box<dyn Registered>> {
    vec![
        Box::new(build_failures::BuildFailures),
        // almanac:register
    ]
}
