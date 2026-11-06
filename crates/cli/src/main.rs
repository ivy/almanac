//! `almanac new NAME` and `almanac generate workflow|prompt NAME`.
use std::{env, fs, path::Path, process::ExitCode};

const CRATE_PATH: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/../almanac");

fn snake(s: &str) -> String {
    s.replace('-', "_")
}

fn camel(s: &str) -> String {
    snake(s)
        .split('_')
        .map(|p| {
            let mut c = p.chars();
            c.next()
                .map_or(String::new(), |f| f.to_uppercase().chain(c).collect())
        })
        .collect()
}

fn write(path: impl AsRef<Path>, content: impl AsRef<str>) -> Result<(), String> {
    let path = path.as_ref();
    fs::write(path, content.as_ref()).map_err(|e| format!("{}: {e}", path.display()))?;
    println!("      create  {}", path.display());
    Ok(())
}

fn new(name: &str) -> Result<(), String> {
    let root = Path::new(name);
    if root.exists() {
        return Err(format!("{name} already exists"));
    }
    fs::create_dir_all(root.join("src/workflows")).map_err(|e| e.to_string())?;
    fs::create_dir_all(root.join("prompts")).map_err(|e| e.to_string())?;
    let crate_path = env::var("ALMANAC_CRATE_PATH").unwrap_or_else(|_| CRATE_PATH.to_string());
    let fill = |t: &str| {
        t.replace("@NAME@", name)
            .replace("@MODULE@", &snake(name))
            .replace("@CRATE_PATH@", &crate_path)
    };
    write(
        root.join("Cargo.toml"),
        fill(include_str!("../templates/Cargo.toml.tmpl")),
    )?;
    write(
        root.join("almanac.toml"),
        include_str!("../templates/almanac.toml"),
    )?;
    write(
        root.join(".env.example"),
        include_str!("../templates/env.example"),
    )?;
    write(
        root.join(".gitignore"),
        include_str!("../templates/gitignore"),
    )?;
    write(
        root.join("src/main.rs"),
        fill(include_str!("../templates/main.rs.tmpl")),
    )?;
    write(
        root.join("src/lib.rs"),
        include_str!("../templates/lib.rs.tmpl"),
    )?;
    write(
        root.join("src/providers.rs"),
        include_str!("../templates/providers.rs.tmpl"),
    )?;
    write(
        root.join("src/workflows/mod.rs"),
        include_str!("../templates/workflows.rs.tmpl"),
    )
}

fn generate_workflow(name: &str) -> Result<(), String> {
    let module = snake(name);
    let registry_path = Path::new("src/workflows/mod.rs");
    let registry =
        fs::read_to_string(registry_path).map_err(|_| "not inside an almanac".to_string())?;
    let file = format!("src/workflows/{module}.rs");
    if Path::new(&file).exists() {
        return Err(format!("{file} already exists"));
    }
    let body = include_str!("../templates/workflow.rs.tmpl")
        .replace("@KEBAB@", &module.replace('_', "-"))
        .replace("@FUNCTION@", &module);
    write(&file, body)?;
    let updated = registry
        .replace(
            "// almanac:modules",
            &format!("pub mod {module};\n// almanac:modules"),
        )
        .replace(
            "// almanac:register",
            &format!(
                "Box::new({module}::{}),\n        // almanac:register",
                camel(name)
            ),
        );
    fs::write(registry_path, updated).map_err(|e| e.to_string())?;
    println!("      update  src/workflows/mod.rs");
    Ok(())
}

fn generate_prompt(name: &str) -> Result<(), String> {
    if !Path::new("src/workflows/mod.rs").exists() {
        return Err("not inside an almanac".to_string());
    }
    let file = format!("prompts/{name}.md");
    if Path::new(&file).exists() {
        return Err(format!("{file} already exists"));
    }
    write(&file, include_str!("../templates/prompt.md"))
}

fn main() -> ExitCode {
    let args: Vec<String> = env::args().skip(1).collect();
    let result = match args
        .iter()
        .map(String::as_str)
        .collect::<Vec<_>>()
        .as_slice()
    {
        ["new", name] => new(name),
        ["generate", "workflow", name] => generate_workflow(name),
        ["generate", "prompt", name] => generate_prompt(name),
        _ => Err("usage: almanac new NAME | almanac generate workflow|prompt NAME".to_string()),
    };
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            eprintln!("almanac: {e}");
            ExitCode::FAILURE
        }
    }
}
