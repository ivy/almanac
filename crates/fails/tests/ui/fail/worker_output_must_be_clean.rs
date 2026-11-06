use fails::*;

async fn f(cx: &Cx) { let _: Result<String, _> = cx.agent(WRITER).act(WritePrompt, &true).await; }
fn main() {}
