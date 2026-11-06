use fails::*;

async fn f(cx: &Cx) { let _: Result<Done, _> = cx.agent(WRITER).act(WritePrompt, &hostile()).await; }
fn main() {}
