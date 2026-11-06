use almanac::prelude::*;

pub mod build_failures;
pub mod security_fixes;
// almanac:modules

pub fn all() -> Vec<Box<dyn Registered>> {
    vec![
        Box::new(build_failures::BuildFailures),
        Box::new(security_fixes::SecurityFixes),
        // almanac:register
    ]
}
