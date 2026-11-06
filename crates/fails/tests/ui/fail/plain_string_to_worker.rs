use fails::*;

async fn f(cx: &Cx) { let _: Result<Done, _> = cx.agent(WRITER).act(WritePrompt, &String::new()).await; }
fn main() {}
