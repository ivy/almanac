use fails::*;

async fn f(cx: &Cx) {
    let out: Untrusted<Done> = cx.agent(READER).read(ReadPrompt, &true).await.unwrap();
    let _: Result<Done, _> = cx.agent(WRITER).act(WritePrompt, &out).await;
}
fn main() {}
