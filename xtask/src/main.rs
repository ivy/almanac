//! `cargo xtask generate` rewrites generated code. `--check` fails if it is stale.
use std::io::Write;
use std::process::{Command, ExitCode, Stdio};

const SPEC: &str = "specs/github/api.github.com.json";
const PROJECTION: &str = "crates/github/projection.toml";
const OUTPUT: &str = "crates/github/src/events.rs";

fn formatted(source: &str) -> Result<String, String> {
    let mut child = Command::new("rustfmt")
        .args(["--edition", "2024", "--emit", "stdout", "--quiet"])
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .map_err(|e| format!("cannot run rustfmt: {e}"))?;
    let mut stdin = child.stdin.take().ok_or("rustfmt has no stdin")?;
    stdin
        .write_all(source.as_bytes())
        .map_err(|e| e.to_string())?;
    drop(stdin);
    let out = child.wait_with_output().map_err(|e| e.to_string())?;
    if !out.status.success() {
        return Err("rustfmt refused the generated code".to_string());
    }
    String::from_utf8(out.stdout).map_err(|e| e.to_string())
}

fn generated() -> Result<String, String> {
    let spec = std::fs::read_to_string(SPEC).map_err(|e| format!("{SPEC}: {e}"))?;
    let spec = serde_json::from_str(&spec).map_err(|e| format!("{SPEC}: {e}"))?;
    let projection =
        std::fs::read_to_string(PROJECTION).map_err(|e| format!("{PROJECTION}: {e}"))?;
    let source = xtask::generate(&spec, &projection).map_err(|e| e.to_string())?;
    formatted(&source)
}

fn run(args: &[String]) -> Result<(), String> {
    let args: Vec<&str> = args.iter().map(String::as_str).collect();
    match args.as_slice() {
        ["generate"] => {
            let source = generated()?;
            std::fs::write(OUTPUT, source).map_err(|e| format!("{OUTPUT}: {e}"))
        }
        ["generate", "--check"] => {
            let current = std::fs::read_to_string(OUTPUT).map_err(|e| format!("{OUTPUT}: {e}"))?;
            if generated()? != current {
                return Err(format!("{OUTPUT} is stale: run `make generate`"));
            }
            Ok(())
        }
        _ => Err("usage: cargo xtask generate [--check]".to_string()),
    }
}

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match run(&args) {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("xtask: {e}");
            ExitCode::FAILURE
        }
    }
}
