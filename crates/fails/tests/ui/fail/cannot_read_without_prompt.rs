use fails::*;

async fn f(cx: &Cx) { let _: Result<Untrusted<Done>, _> = cx.agent(READER).read(&true).await; }
fn main() {}
