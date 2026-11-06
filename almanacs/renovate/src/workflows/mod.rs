use almanac::prelude::*;

pub mod dependency_updates;
// almanac:modules

pub fn all() -> Vec<Box<dyn Registered>> {
    vec![
        Box::new(dependency_updates::DependencyUpdates),
        // almanac:register
    ]
}
