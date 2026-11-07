use almanac::config::Config;
use almanac::diag::{Error, Format, PAGES, explain, render};
use almanac::prelude::*;
use almanac::runtime::Runtime;
use almanac::testing::generators::{config_texts, error_codes};
use hegel::TestCase;
use hegel::generators as gs;
use serde_json::Value;

fn parse(text: &str) -> Error {
    Config::parse("almanac.toml", text.to_string()).unwrap_err()
}

#[test]
fn a_secret_in_config_points_at_its_line_and_says_where_it_belongs() {
    let error = parse("github_token = \"abc\"\n");
    assert!(matches!(error, Error::SecretInConfig { .. }), "{error:?}");
    insta::assert_snapshot!(render(&error, Format::Human));
}

#[test]
fn a_secret_is_never_printed_by_the_error_about_it() {
    let error = parse("listen = \"x\"\napi_key = \"hunter2\"\n");
    for format in [Format::Human, Format::Json] {
        let rendered = render(&error, format);
        assert!(!rendered.contains("hunter2"), "{rendered}");
    }
}

#[test]
fn an_unknown_key_is_named_and_lists_what_is_accepted() {
    let error = parse("listen = \"127.0.0.1:1\"\nmax_runs = 3\n");
    assert!(matches!(error, Error::UnknownKey { .. }), "{error:?}");
    insta::assert_snapshot!(render(&error, Format::Human));
}

#[test]
fn a_wrong_type_points_at_the_value() {
    let error = parse("concurrency = \"many\"\n");
    assert!(matches!(error, Error::ConfigParse { .. }), "{error:?}");
    insta::assert_snapshot!(render(&error, Format::Human));
}

#[test]
fn json_output_carries_code_message_help_and_spans() {
    let rendered = render(&parse("github_token = \"abc\"\n"), Format::Json);
    let json: Value = serde_json::from_str(&rendered).unwrap();
    assert_eq!(json["code"], "almanac::config::secret");
    assert!(json["message"].as_str().unwrap().contains("environment"));
    assert!(!json["help"].as_str().unwrap().is_empty());
    let span = &json["labels"][0]["span"];
    assert_eq!(span["offset"], 0);
    assert_eq!(span["length"], 12);
}

#[workflow(on = NoEvent, key = "{id}", intent = "x", may = [Await], runs_per_hour = 1, decide = nothing)]
async fn needs_a_provider(cx: &Cx, _event: &NoEvent) -> Result<(), Halt> {
    cx.get::<String>()?;
    Ok(())
}

fn nothing(_: &()) -> Vec<Effect> {
    vec![]
}

observed! {
    pub struct NoEvent { pub id: u64 }
}

impl Event for NoEvent {
    const KIND: &'static str = "none";
    fn parse(p: &Payload<'_>) -> Result<Self, Malformed> {
        Ok(Self {
            id: p.field("/id")?,
        })
    }
}

#[test]
fn an_agent_can_read_which_provider_is_missing() {
    let runtime = Runtime::new(vec![Box::new(NeedsAProvider)], Providers::new());
    let error = runtime.validate().unwrap_err();
    let json: Value = serde_json::from_str(&render(&error, Format::Json)).unwrap();
    assert_eq!(json["code"], "almanac::provider::missing");
    let message = json["message"].as_str().unwrap();
    assert!(message.contains("needs a provider for"), "{message}");
    assert!(message.contains("String"), "{message}");
}

/// Letters and digits only, so wrapping, box drawing, and indentation do not count as substance.
fn substance(text: &str) -> String {
    text.chars().filter(|c| c.is_alphanumeric()).collect()
}

/// Principle 9: an error renders a known code, a message, and help, and its human and JSON
/// forms say the same things.
fn assert_renders_alike(error: &Error) {
    let json: Value = serde_json::from_str(&render(error, Format::Json)).unwrap();
    let human = substance(&render(error, Format::Human));
    let code = json["code"].as_str().expect("a code");
    assert!(explain(code).is_ok(), "{code} has no page");
    for field in ["message", "help"] {
        let text = json[field].as_str().unwrap_or_default();
        assert!(!text.trim().is_empty(), "{code} has no {field}");
        assert!(
            human.contains(&substance(text)),
            "{code}'s human form lacks its {field}"
        );
    }
    assert!(
        human.contains(&substance(code)),
        "{code} is missing from the human form"
    );
}

/// Principle 9, R-42: whatever a config file says, an error about it has a code, a page, and
/// help, in the same substance for people and for agents.
#[hegel::test]
fn principle_9_every_config_error_says_what_to_do_in_both_forms(tc: TestCase) {
    let text = tc.draw(config_texts());
    if let Err(error) = Config::parse("almanac.toml", text) {
        assert_renders_alike(&error);
    }
}

/// Principle 9, R-42: `explain` answers any code with its page, or with an error that is
/// itself explained.
#[hegel::test]
fn principle_9_explain_answers_any_code(tc: TestCase) {
    let code = tc.draw(error_codes());
    match explain(&code) {
        Ok(page) => assert!(page.contains("## What to do next"), "{code}"),
        Err(error) => assert_renders_alike(&error),
    }
}

/// R-44: a secret written into config is never printed by the error about it.
#[hegel::test]
fn a_secret_in_config_is_never_printed(tc: TestCase) {
    let text = tc.draw(config_texts());
    let secret = tc.draw(gs::from_regex("[A-Za-z0-9]{8,16}"));
    let text = format!("{text}\ngithub_token = \"{secret}\"\n");
    let error = Config::parse("almanac.toml", text).unwrap_err();
    for format in [Format::Human, Format::Json] {
        assert!(!render(&error, format).contains(&secret), "{format:?}");
    }
}

/// Principle 9: every listed code has a page in `docs/errors` named for it, and every page
/// there is listed.
#[test]
fn every_code_has_a_page_and_every_page_a_code() {
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/../../docs/errors");
    let on_disk: std::collections::BTreeSet<String> = std::fs::read_dir(dir)
        .unwrap()
        .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    let listed = PAGES
        .iter()
        .map(|(code, _)| format!("{}.md", code.replace("::", ".")))
        .collect();
    assert_eq!(on_disk, listed);
    for (code, page) in PAGES {
        assert!(page.contains("## What to do next"), "{code}");
    }
}

/// Principle 9: human output is safe to log, as `render` promises, so no control character
/// from input reaches a terminal, whatever a config file or a code says.
#[hegel::test]
fn principle_9_a_human_error_carries_no_control_characters(tc: TestCase) {
    let error = match tc.draw(gs::booleans()) {
        true => explain(&tc.draw(error_codes())).err(),
        false => Config::parse("almanac.toml", tc.draw(config_texts())).err(),
    };
    tc.assume(error.is_some());
    let human = render(&error.unwrap(), Format::Human);
    let control = human.chars().find(|c| c.is_control() && *c != '\n');
    assert_eq!(control, None, "{human:?}");
}
