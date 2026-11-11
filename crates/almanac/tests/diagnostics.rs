use almanac::config::Config;
use almanac::diag::{Error, Format, render};
use almanac::prelude::*;
use almanac::runtime::Runtime;
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

#[test]
fn human_output_shows_control_characters_as_pictures() {
    let errors = [
        parse("listen = \"\u{1b}[31m\"\nfoo = 1\n"),
        parse("x\u{7}y = 1\r\n"),
        almanac::diag::explain("a\u{1}b\u{7f}\u{9b}").unwrap_err(),
    ];
    for error in errors {
        let human = render(&error, Format::Human);
        let control = human.chars().find(|c| c.is_control() && *c != '\n');
        assert_eq!(control, None, "{human:?}");
    }
    let human = render(
        &almanac::diag::explain("a\u{1b}b").unwrap_err(),
        Format::Human,
    );
    assert!(human.contains("a\u{241b}b"), "{human:?}");
}
