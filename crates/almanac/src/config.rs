//! Runtime configuration: a file, then `ALMANAC_*` environment variables on top.
//! Behaviour lives in the binary. Config can tighten limits but never widen them,
//! and it has no way to name a capability, an effect, or a secret.
use crate::runtime::Limits;
use serde::Deserialize;
use std::{env, fs, path::Path, str::FromStr};

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub listen: Option<String>,
    pub max_chain_depth: Option<u32>,
    pub runs_per_hour_cap: Option<u32>,
    pub concurrency: Option<usize>,
}

fn from_env<T: FromStr>(name: &str, slot: &mut Option<T>) -> Result<(), String> {
    if let Ok(v) = env::var(name) {
        *slot = Some(v.parse().map_err(|_| format!("{name}: not a number"))?);
    }
    Ok(())
}

impl Config {
    pub fn load() -> Result<Self, String> {
        let path = env::var("ALMANAC_CONFIG").unwrap_or_else(|_| "almanac.toml".to_string());
        let mut c: Config = if Path::new(&path).exists() {
            let text = fs::read_to_string(&path).map_err(|e| format!("{path}: {e}"))?;
            toml::from_str(&text).map_err(|e| format!("{path}: {e}"))?
        } else {
            Config::default()
        };
        if let Ok(v) = env::var("ALMANAC_LISTEN") {
            c.listen = Some(v);
        }
        from_env("ALMANAC_MAX_CHAIN_DEPTH", &mut c.max_chain_depth)?;
        from_env("ALMANAC_RUNS_PER_HOUR_CAP", &mut c.runs_per_hour_cap)?;
        from_env("ALMANAC_CONCURRENCY", &mut c.concurrency)?;
        Ok(c)
    }

    pub fn limits(&self) -> Limits {
        let d = Limits::default();
        Limits {
            max_depth: self.max_chain_depth.unwrap_or(d.max_depth),
            runs_cap: self.runs_per_hour_cap,
            concurrency: self.concurrency.unwrap_or(d.concurrency),
        }
    }
}
