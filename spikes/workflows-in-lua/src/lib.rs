//! A spike: Almanac workflows defined in Luau, with Rust as the trusted host.
#![forbid(unsafe_code)]

pub mod catalog;
pub mod declare;
pub mod effects;
pub mod renovate;
pub mod sandbox;
pub mod taint;
pub mod value;
pub mod workflow;
