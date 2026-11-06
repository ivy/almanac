//! Runtime configuration: a file, then `ALMANAC_*` environment variables on top.
//! Behaviour lives in the binary. Config can tighten limits but never widen them,
//! and it has no way to name a capability, an effect, or a secret.
use crate::diag::Error;
use crate::runtime::Limits;
use miette::NamedSource;
use serde::Deserialize;
use std::{env, fs, path::Path, str::FromStr};

const SECRET_WORDS: [&str; 5] = ["token", "secret", "password", "credential", "key"];

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub listen: Option<String>,
    pub max_chain_depth: Option<u32>,
    pub runs_per_hour_cap: Option<u32>,
    pub overall_runs_per_hour_cap: Option<u32>,
    pub concurrency: Option<usize>,
}

fn from_env<T: FromStr>(name: &str, slot: &mut Option<T>) -> Result<(), Error> {
    if let Ok(v) = env::var(name) {
        let invalid = |_| Error::EnvValue {
            name: name.to_string(),
        };
        *slot = Some(v.parse().map_err(invalid)?);
    }
    Ok(())
}

impl Config {
    pub fn load() -> Result<Self, Error> {
        let path = env::var("ALMANAC_CONFIG").unwrap_or_else(|_| "almanac.toml".to_string());
        let mut c = if Path::new(&path).exists() {
            let text = fs::read_to_string(&path).map_err(|source| Error::ConfigRead {
                path: path.clone(),
                source,
            })?;
            Self::parse(&path, text)?
        } else {
            Config::default()
        };
        if let Ok(v) = env::var("ALMANAC_LISTEN") {
            c.listen = Some(v);
        }
        from_env("ALMANAC_MAX_CHAIN_DEPTH", &mut c.max_chain_depth)?;
        from_env("ALMANAC_RUNS_PER_HOUR_CAP", &mut c.runs_per_hour_cap)?;
        from_env(
            "ALMANAC_OVERALL_RUNS_PER_HOUR_CAP",
            &mut c.overall_runs_per_hour_cap,
        )?;
        from_env("ALMANAC_CONCURRENCY", &mut c.concurrency)?;
        Ok(c)
    }

    /// Parses a config file's text, pointing any error at the line that caused it.
    pub fn parse(name: &str, text: String) -> Result<Self, Error> {
        let e = match toml::from_str(&text) {
            Ok(config) => return Ok(config),
            Err(e) => e,
        };
        let range = e.span();
        let span = range.clone().map(Into::into);
        let key = range
            .filter(|_| e.message().starts_with("unknown field"))
            .and_then(|r| text.get(r))
            .map(str::to_string);
        if let Some(key) = key.as_ref().filter(|key| looks_secret(key)) {
            return Err(Error::SecretInConfig {
                key: key.clone(),
                source_code: NamedSource::new(name, redacted(&text, key)),
                span,
            });
        }
        let source_code = NamedSource::new(name, text);
        Err(match key {
            Some(key) => Error::UnknownKey {
                key,
                source_code,
                span,
            },
            None => Error::ConfigParse {
                message: e.message().to_string(),
                source_code,
                span,
            },
        })
    }

    pub fn limits(&self) -> Limits {
        let d = Limits::default();
        Limits {
            max_depth: self.max_chain_depth.unwrap_or(d.max_depth),
            runs_cap: self.runs_per_hour_cap,
            overall_cap: self.overall_runs_per_hour_cap,
            concurrency: self.concurrency.unwrap_or(d.concurrency),
        }
    }
}

/// The file with every value of `key` hidden, so an error cannot print the secret it warns about.
/// The key keeps its position, because the error's span points at it.
fn redacted(text: &str, key: &str) -> String {
    let hide = |line: &str| match line.trim_start().strip_prefix(key) {
        Some(_) => format!(
            "{} = <redacted>",
            line.split('=').next().unwrap_or(line).trim_end()
        ),
        None => line.to_string(),
    };
    text.lines().map(hide).collect::<Vec<_>>().join("\n")
}

fn looks_secret(key: &str) -> bool {
    let key = key.to_lowercase();
    SECRET_WORDS.iter().any(|word| key.contains(word))
}
