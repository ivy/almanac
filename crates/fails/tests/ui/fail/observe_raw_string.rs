use fails::*;

async fn f(cx: &Cx) { let _ = cx.observe("x", async { Ok(String::new()) }).await; }
fn main() {}
