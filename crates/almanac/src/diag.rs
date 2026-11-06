//! Every error a person or agent can meet: what happened, why it matters, and what to do next.
//! Codes are stable, and each has a page in `docs/errors`, shown by `explain`.
use crate::store::StoreError;
use miette::SourceSpan;
use miette::{Diagnostic, GraphicalReportHandler, GraphicalTheme, JSONReportHandler, NamedSource};
use thiserror::Error;

macro_rules! pages {
    ($($code:literal => $file:literal),* $(,)?) => {
        /// Every code, with its long-form text.
        pub const PAGES: &[(&str, &str)] = &[
            $(($code, include_str!(concat!("../../../docs/errors/", $file, ".md")))),*
        ];
    };
}

pages! {
    "almanac::config::read" => "almanac.config.read",
    "almanac::config::parse" => "almanac.config.parse",
    "almanac::config::unknown_key" => "almanac.config.unknown_key",
    "almanac::config::secret" => "almanac.config.secret",
    "almanac::config::env_value" => "almanac.config.env_value",
    "almanac::usage" => "almanac.usage",
    "almanac::provider::missing" => "almanac.provider.missing",
    "almanac::store::failed" => "almanac.store.failed",
    "almanac::input::unreadable" => "almanac.input.unreadable",
    "almanac::input::invalid_json" => "almanac.input.invalid_json",
    "almanac::runtime::startup" => "almanac.runtime.startup",
    "almanac::explain::unknown_code" => "almanac.explain.unknown_code",
}

/// The long form of a code.
pub fn explain(code: &str) -> Result<&'static str, Error> {
    PAGES
        .iter()
        .find(|(known, _)| *known == code)
        .map(|(_, page)| *page)
        .ok_or_else(|| Error::UnknownCode {
            code: code.to_string(),
        })
}

#[derive(Debug, Error, Diagnostic)]
pub enum Error {
    #[error("cannot read config file {path}")]
    #[diagnostic(
        code(almanac::config::read),
        help("check the path and its permissions, or unset ALMANAC_CONFIG to run with defaults")
    )]
    ConfigRead {
        path: String,
        #[source]
        source: std::io::Error,
    },

    #[error("config file is not valid: {message}")]
    #[diagnostic(
        code(almanac::config::parse),
        help("fix the marked line; run `explain almanac::config::parse` for the accepted keys")
    )]
    ConfigParse {
        message: String,
        #[source_code]
        source_code: NamedSource<String>,
        #[label("here")]
        span: Option<SourceSpan>,
    },

    #[error("unknown config key `{key}`")]
    #[diagnostic(
        code(almanac::config::unknown_key),
        help(
            "config can only tighten limits: the keys are listen, max_chain_depth, runs_per_hour_cap, overall_runs_per_hour_cap, and concurrency"
        )
    )]
    UnknownKey {
        key: String,
        #[source_code]
        source_code: NamedSource<String>,
        #[label("Almanac does not read this")]
        span: Option<SourceSpan>,
    },

    #[error("`{key}` looks like a secret, and secrets come from the environment")]
    #[diagnostic(
        code(almanac::config::secret),
        help(
            "remove it from the file and set it in the environment; rotate it if it was committed"
        )
    )]
    SecretInConfig {
        key: String,
        #[source_code]
        source_code: NamedSource<String>,
        #[label("move this to the environment")]
        span: Option<SourceSpan>,
    },

    #[error("{name} must be a whole number")]
    #[diagnostic(
        code(almanac::config::env_value),
        help("set {name} to a whole number, or unset it")
    )]
    EnvValue { name: String },

    #[error("not a command: {given}")]
    #[diagnostic(code(almanac::usage), help("{usage}"))]
    Usage { given: String, usage: String },

    #[error("{}", missing.join("; "))]
    #[diagnostic(
        code(almanac::provider::missing),
        help("add what is missing in providers.rs")
    )]
    MissingProviders { missing: Vec<String> },

    #[error(transparent)]
    #[diagnostic(
        code(almanac::store::failed),
        help(
            "check DATABASE_URL and that PostgreSQL is running; undelivered events will be retried"
        )
    )]
    Store(#[from] StoreError),

    #[error("cannot read {path}")]
    #[diagnostic(
        code(almanac::input::unreadable),
        help("check the path and its permissions")
    )]
    InputUnreadable {
        path: String,
        #[source]
        source: std::io::Error,
    },

    #[error("{path} is not valid JSON")]
    #[diagnostic(
        code(almanac::input::invalid_json),
        help("pass the raw JSON body of the event")
    )]
    InputInvalidJson {
        path: String,
        #[source]
        source: serde_json::Error,
    },

    #[error("cannot start the runtime")]
    #[diagnostic(
        code(almanac::runtime::startup),
        help("check the host's limits on threads and open files")
    )]
    Startup {
        #[source]
        source: std::io::Error,
    },

    #[error("no error has the code `{code}`")]
    #[diagnostic(
        code(almanac::explain::unknown_code),
        help("copy the code exactly from the error, such as almanac::config::unknown_key")
    )]
    UnknownCode { code: String },
}

/// How output is written. Agents ask for `Json`.
#[derive(Clone, Copy, Debug, Default, PartialEq)]
pub enum Format {
    #[default]
    Human,
    Json,
}

/// Renders an error. Human output has no colour, so it is stable and safe to log.
pub fn render(error: &dyn Diagnostic, format: Format) -> String {
    match format {
        Format::Json => {
            let mut out = String::new();
            let handler = JSONReportHandler::new();
            match handler.render_report(&mut out, error) {
                Ok(()) => out,
                Err(_) => serde_json::json!({"message": error.to_string()}).to_string(),
            }
        }
        Format::Human => {
            let handler = GraphicalReportHandler::new_themed(GraphicalTheme::unicode_nocolor())
                .with_width(100);
            let mut out = String::new();
            match handler.render_report(&mut out, error) {
                Ok(()) => out,
                Err(_) => error.to_string(),
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn source() -> NamedSource<String> {
        NamedSource::new("almanac.toml", String::new())
    }

    fn io() -> std::io::Error {
        std::io::Error::other("x")
    }

    /// One of each. The match has no wildcard, so a new variant fails to compile until it is
    /// listed here and so held to the test below.
    fn samples() -> Vec<Error> {
        let all = vec![
            Error::ConfigRead {
                path: "p".into(),
                source: io(),
            },
            Error::ConfigParse {
                message: "m".into(),
                source_code: source(),
                span: None,
            },
            Error::UnknownKey {
                key: "k".into(),
                source_code: source(),
                span: None,
            },
            Error::SecretInConfig {
                key: "k".into(),
                source_code: source(),
                span: None,
            },
            Error::EnvValue { name: "n".into() },
            Error::Usage {
                given: "g".into(),
                usage: "u".into(),
            },
            Error::MissingProviders { missing: vec![] },
            Error::Store(StoreError("s".into())),
            Error::InputUnreadable {
                path: "p".into(),
                source: io(),
            },
            Error::InputInvalidJson {
                path: "p".into(),
                source: serde_json::from_str::<u8>("x").unwrap_err(),
            },
            Error::Startup { source: io() },
            Error::UnknownCode { code: "c".into() },
        ];
        for e in &all {
            match e {
                Error::ConfigRead { .. }
                | Error::ConfigParse { .. }
                | Error::UnknownKey { .. }
                | Error::SecretInConfig { .. }
                | Error::EnvValue { .. }
                | Error::Usage { .. }
                | Error::MissingProviders { .. }
                | Error::Store(_)
                | Error::InputUnreadable { .. }
                | Error::InputInvalidJson { .. }
                | Error::Startup { .. }
                | Error::UnknownCode { .. } => {}
            }
        }
        all
    }

    #[test]
    fn every_error_has_a_page_a_code_and_help() {
        let samples = samples();
        assert_eq!(samples.len(), PAGES.len());
        for e in samples {
            let code = e.code().expect("a code").to_string();
            let page = explain(&code).unwrap_or_else(|_| panic!("{code} has no page"));
            assert!(page.contains("## What to do next"), "{code}");
            let help = e.help().expect("help").to_string();
            assert!(!help.trim().is_empty(), "{code}");
        }
    }

    #[test]
    fn every_page_on_disk_is_listed() {
        let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/../../docs/errors");
        let pages = std::fs::read_dir(dir).unwrap().count();
        assert_eq!(pages, PAGES.len());
    }

    #[test]
    fn an_unknown_code_is_itself_explained() {
        let e = explain("nonsense").unwrap_err();
        assert_eq!(
            e.code().unwrap().to_string(),
            "almanac::explain::unknown_code"
        );
    }
}
