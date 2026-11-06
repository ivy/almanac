use fails::*;

async fn f(cx: &Cx) { let _: Result<Done, _> = cx.agent(READER).act(ReadPrompt, &true).await; }
fn main() {}
