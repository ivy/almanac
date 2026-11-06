//! Taint tracking. Observed text is `Untrusted`; `Clean` values cannot carry free text.
use serde::{Deserialize, Serialize};
use std::{fmt, str::FromStr};

/// Observed content. No `Display`, no accessor: the ways out are `narrow`, which must
/// produce a `Finite` value, and `map`, which stays tainted.
#[derive(Clone, Serialize, Deserialize)]
pub struct Untrusted<T> {
    #[serde(rename = "$untrusted")]
    inner: T,
}

impl<T> Untrusted<T> {
    pub fn new(inner: T) -> Self {
        Self { inner }
    }

    /// Escape taint by narrowing to a closed set of values. Takes a `fn`, not a closure,
    /// so it cannot capture a local and smuggle the content out.
    pub fn narrow<N: Finite>(&self, f: fn(&T) -> N) -> N {
        f(&self.inner)
    }

    pub fn map<U>(&self, f: fn(&T) -> U) -> Untrusted<U> {
        Untrusted::new(f(&self.inner))
    }
}

impl<T> fmt::Debug for Untrusted<T> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("Untrusted(..)")
    }
}

#[doc(hidden)]
pub mod __private {
    pub use serde;
    /// Hand-writing this is the deliberate, greppable way around the seal.
    pub trait Sealed {}
    pub trait SealedFinite {}
    pub trait SealedObserved {}
    /// A field allowed in an `observed!` struct: `Clean`, or explicitly `Untrusted`.
    pub trait Field {}
}

/// A value with no attacker-controlled free text. Sealed: use `clean!`.
pub trait Clean: __private::Sealed {}

/// A closed set of values: the only thing tainted content can narrow to.
pub trait Finite: Clean + __private::SealedFinite {}

/// A type read from the outside world, whose every field is `Clean` or `Untrusted`.
pub trait Observed: __private::SealedObserved {}

impl<T: Clean> __private::Field for T {}
impl<T> __private::Field for Untrusted<T> {}

macro_rules! seal {
    ($($t:ty),*) => { $(impl __private::Sealed for $t {} impl Clean for $t {})* };
}
seal!(bool, u32, u64, f64, Slug, Sha);
impl<T: Clean> __private::Sealed for Option<T> {}
impl<T: Clean> Clean for Option<T> {}
impl __private::SealedFinite for bool {}
impl Finite for bool {}

/// A string restricted to a validated shape.
macro_rules! validated {
    ($(#[$m:meta])* $name:ident, $what:literal, $valid:expr) => {
        $(#[$m])*
        #[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
        #[serde(try_from = "String", into = "String")]
        pub struct $name(String);

        impl TryFrom<String> for $name {
            type Error = String;
            fn try_from(s: String) -> Result<Self, String> {
                let valid: fn(&str) -> bool = $valid;
                valid(&s).then_some(Self(s)).ok_or_else(|| concat!("not a ", $what).to_string())
            }
        }
        impl FromStr for $name {
            type Err = String;
            fn from_str(s: &str) -> Result<Self, String> {
                s.to_string().try_into()
            }
        }
        impl From<$name> for String {
            fn from(s: $name) -> String {
                s.0
            }
        }
        impl fmt::Display for $name {
            fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                f.write_str(&self.0)
            }
        }
    };
}

validated!(
    /// An identifier with a restricted alphabet: repo names, branches, logins.
    Slug, "slug",
    |s| !s.is_empty() && s.len() <= 200 && s.chars().all(|c| c.is_ascii_alphanumeric() || "._/-:@[]".contains(c))
);
validated!(
    /// A full commit hash.
    Sha, "sha",
    |s| s.len() == 40 && s.chars().all(|c| c.is_ascii_hexdigit())
);

/// Declare a struct or fieldless enum that is `Clean`. Each struct field must be too.
#[macro_export]
macro_rules! clean {
    ($(#[$m:meta])* $v:vis struct $n:ident { $($fv:vis $f:ident : $t:ty),* $(,)? }) => {
        #[derive(Clone, Debug, PartialEq, $crate::__private::serde::Serialize, $crate::__private::serde::Deserialize)]
        #[serde(crate = "almanac::__private::serde")]
        $(#[$m])* $v struct $n { $($fv $f: $t),* }
        impl $crate::__private::Sealed for $n {}
        impl $crate::taint::Clean for $n {}
        const _: fn() = || { fn check<T: $crate::taint::Clean>() {} $(check::<$t>();)* };
    };
    ($(#[$m:meta])* $v:vis enum $n:ident { $($(#[$vm:meta])* $var:ident),* $(,)? }) => {
        #[derive(Clone, Copy, Debug, PartialEq, $crate::__private::serde::Serialize, $crate::__private::serde::Deserialize)]
        #[serde(crate = "almanac::__private::serde")]
        $(#[$m])* $v enum $n { $($(#[$vm])* $var),* }
        impl $crate::__private::Sealed for $n {}
        impl $crate::taint::Clean for $n {}
        impl $crate::__private::SealedFinite for $n {}
        impl $crate::taint::Finite for $n {}
    };
}

/// Declare a type read from outside. A bare `String` field does not compile.
#[macro_export]
macro_rules! observed {
    ($(#[$m:meta])* $v:vis struct $n:ident { $($fv:vis $f:ident : $t:ty),* $(,)? }) => {
        #[derive(Clone, Debug, $crate::__private::serde::Serialize, $crate::__private::serde::Deserialize)]
        #[serde(crate = "almanac::__private::serde")]
        $(#[$m])* $v struct $n { $($fv $f: $t),* }
        impl $crate::__private::SealedObserved for $n {}
        impl $crate::taint::Observed for $n {}
        const _: fn() = || { fn check<T: $crate::__private::Field>() {} $(check::<$t>();)* };
    };
}

/// Declare what an agent returns. It arrives wrapped in `Untrusted`, so it may hold text.
#[macro_export]
macro_rules! output {
    ($(#[$m:meta])* $v:vis struct $n:ident { $($fv:vis $f:ident : $t:ty),* $(,)? }) => {
        #[derive(Clone, $crate::__private::serde::Serialize, $crate::__private::serde::Deserialize)]
        #[serde(crate = "almanac::__private::serde")]
        $(#[$m])* $v struct $n { $($fv $f: $t),* }
    };
}
