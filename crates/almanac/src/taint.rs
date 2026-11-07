//! Taint tracking. Observed text is `Untrusted`; `Clean` values cannot carry free text.
use serde::{Deserialize, Serialize};
use std::{fmt, str::FromStr};

/// Observed content. No `Display`, no accessor: the ways out are `narrow`, which must
/// produce a `Finite` value, and `map` and `join`, which stay tainted.
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

    /// For checks inside this crate that decide with code. Never public.
    pub(crate) fn inner(&self) -> &T {
        &self.inner
    }

    pub fn map<U>(&self, f: fn(&T) -> U) -> Untrusted<U> {
        Untrusted::new(f(&self.inner))
    }
}

impl Untrusted<String> {
    /// `clean` followed by `tainted`. The whole is tainted, so this only widens taint.
    pub fn join(clean: &str, tainted: &Untrusted<String>) -> Self {
        Self::new(format!("{clean}{}", tainted.inner))
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
impl<T: Clean> __private::Sealed for &T {}
impl<T: Clean> Clean for &T {}
impl<T: Clean> __private::Sealed for Option<T> {}
impl<T: Clean> Clean for Option<T> {}
impl<T: Clean> __private::Sealed for Vec<T> {}
impl<T: Clean> Clean for Vec<T> {}
impl __private::SealedFinite for bool {}
impl Finite for bool {}

/// Text that failed validation. It names the shape and never repeats the text, which may be
/// hostile.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Invalid(pub &'static str);

impl fmt::Display for Invalid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "not a {}", self.0)
    }
}

impl std::error::Error for Invalid {}

/// A string restricted to a validated shape.
macro_rules! validated {
    ($(#[$m:meta])* $name:ident, $what:literal, $valid:expr) => {
        $(#[$m])*
        #[derive(Clone, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
        #[serde(try_from = "String", into = "String")]
        pub struct $name(String);

        impl TryFrom<String> for $name {
            type Error = Invalid;
            fn try_from(s: String) -> Result<Self, Invalid> {
                let valid: fn(&str) -> bool = $valid;
                valid(&s).then_some(Self(s)).ok_or(Invalid($what))
            }
        }
        impl FromStr for $name {
            type Err = Invalid;
            fn from_str(s: &str) -> Result<Self, Invalid> {
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

/// Text a tracker shows as it is: an issue's title, or a question. Only `title!` builds one, so
/// it holds nothing but a literal and `Clean` values.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Title(String);

impl Title {
    #[doc(hidden)]
    pub fn __compose(text: String) -> Self {
        Self(text)
    }
}

impl fmt::Display for Title {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

#[doc(hidden)]
pub fn __clean<T: Clean + fmt::Display>(value: &T) -> &T {
    value
}

/// Whether every placeholder is a bare `{}`. A named one would read a variable that `title!`
/// never sees, so it could not check it.
#[doc(hidden)]
pub const fn __bare_placeholders(format: &str) -> bool {
    let bytes = format.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'{' {
            if i + 1 >= bytes.len() || (bytes[i + 1] != b'}' && bytes[i + 1] != b'{') {
                return false;
            }
            i += 1;
        }
        i += 1;
    }
    true
}

/// Build a `Title` from a literal and `Clean` values, with each value passed as an argument:
/// `title!("{} failed {} times", tool, count)`.
#[macro_export]
macro_rules! title {
    ($format:literal $(, $value:expr)* $(,)?) => {{
        const _: () = assert!(
            $crate::taint::__bare_placeholders($format),
            "title! takes each value as an argument, so that it can check it is Clean"
        );
        $crate::taint::Title::__compose(::std::format!(
            $format $(, $crate::taint::__clean(&$value))*
        ))
    }};
}

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
